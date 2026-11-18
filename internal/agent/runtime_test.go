package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/grpc"
)

type sessionTestServer struct {
	pb.UnimplementedAgentServiceServer
	connect func(grpc.BidiStreamingServer[pb.AgentMessage, pb.ServerMessage]) error
}

func (s *sessionTestServer) Connect(stream grpc.BidiStreamingServer[pb.AgentMessage, pb.ServerMessage]) error {
	return s.connect(stream)
}

func startSessionTestServer(t *testing.T, connect func(grpc.BidiStreamingServer[pb.AgentMessage, pb.ServerMessage]) error) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterAgentServiceServer(server, &sessionTestServer{connect: connect})
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

func TestReplayDrainsContinuouslyAndKeepsHeartbeats(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(fmt.Sprintf("slow_center=%v", slow), func(t *testing.T) {
			const records = 10000
			wal, err := OpenWAL(filepath.Join(t.TempDir(), "agent.wal"), 32<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer wal.Close()
			items := make([]*pb.FlowObservation, records)
			for i := range items {
				items[i] = testObservation(fmt.Sprint(i))
			}
			if _, err := wal.AppendBatch(items); err != nil {
				t.Fatal(err)
			}
			type result struct {
				duration                 time.Duration
				heartbeats, capabilities int
				err                      error
			}
			completed := make(chan result, 1)
			address := startSessionTestServer(t, func(stream grpc.BidiStreamingServer[pb.AgentMessage, pb.ServerMessage]) error {
				out := result{}
				defer func() { completed <- out }()
				first, err := stream.Recv()
				if err != nil || first.GetAuthenticate() == nil {
					out.err = fmt.Errorf("authentication missing: %v", err)
					return out.err
				}
				if err := stream.Send(&pb.ServerMessage{Payload: &pb.ServerMessage_Authenticated{Authenticated: &pb.Authenticated{
					NextSequence: 1, HeartbeatIntervalSeconds: 1, SupportsObservationCoverage: true,
				}}}); err != nil {
					out.err = err
					return err
				}
				started := time.Now()
				next := uint64(1)
				for next <= records {
					message, err := stream.Recv()
					if err != nil {
						out.err = err
						return err
					}
					if message.GetHeartbeat() != nil {
						out.heartbeats++
					}
					if message.GetCapabilities() != nil {
						out.capabilities++
					}
					batch := message.GetObservations()
					if batch == nil {
						continue
					}
					if batch.FirstSequence != next || len(batch.Observations) > 500 || batch.LastSequence != next+uint64(len(batch.Observations))-1 {
						out.err = fmt.Errorf("invalid batch at %d: %v", next, batch)
						return out.err
					}
					if slow {
						time.Sleep(65 * time.Millisecond)
					}
					// A config message is not an ACK and must not drop the pending batch.
					if err := stream.Send(&pb.ServerMessage{Payload: &pb.ServerMessage_Config{Config: &pb.ConfigUpdate{}}}); err != nil {
						out.err = err
						return err
					}
					if err := stream.Send(&pb.ServerMessage{Payload: &pb.ServerMessage_Ack{Ack: &pb.BatchAck{AcknowledgedSequence: batch.LastSequence}}}); err != nil {
						out.err = err
						return err
					}
					next = batch.LastSequence + 1
				}
				out.duration = time.Since(started)
				return nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			state := &runtimeState{}
			sessionDone := make(chan error, 1)
			go func() {
				sessionDone <- runSession(ctx, Config{ServerAddress: address, DeviceID: "device", DeviceCredential: "test"}, wal, state, make(chan struct{}))
			}()
			var out result
			select {
			case out = <-completed:
			case err := <-sessionDone:
				t.Fatalf("session stopped before replay completed: %v", err)
			case <-ctx.Done():
				t.Fatal("replay timed out")
			}
			if out.err != nil {
				t.Fatal(out.err)
			}
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				pending, _, _, _, _ := wal.Stats()
				if pending == 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("final ACK was not applied")
				case <-ticker.C:
				}
			}
			cancel()
			err = <-sessionDone
			if pending, _, _, acked, _ := wal.Stats(); pending != 0 || acked != records {
				t.Fatalf("replay pending=%d acked=%d error=%v", pending, acked, err)
			}
			if !state.supportsCoverage() {
				t.Fatal("coverage handshake was not recorded")
			}
			if !slow && out.duration >= 2500*time.Millisecond {
				t.Fatalf("20 replay batches took %s; the former 200ms per-batch pause should be gone", out.duration)
			}
			if slow && out.heartbeats == 0 {
				t.Fatal("replay starved heartbeats")
			}
			if out.capabilities > 3 {
				t.Fatalf("capability updates flooded Center: %d", out.capabilities)
			}
			t.Logf("10000 observations: replay=%s, heartbeats=%d, capability reports=%d", out.duration, out.heartbeats, out.capabilities)
		})
	}
}

func TestReplayRejectsWrongAckWithoutDroppingPendingRecords(t *testing.T) {
	wal, err := OpenWAL(filepath.Join(t.TempDir(), "agent.wal"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	if _, err := wal.AppendBatch([]*pb.FlowObservation{testObservation("one"), testObservation("two")}); err != nil {
		t.Fatal(err)
	}
	address := startSessionTestServer(t, func(stream grpc.BidiStreamingServer[pb.AgentMessage, pb.ServerMessage]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		if err := stream.Send(&pb.ServerMessage{Payload: &pb.ServerMessage_Authenticated{Authenticated: &pb.Authenticated{NextSequence: 1}}}); err != nil {
			return err
		}
		for {
			message, err := stream.Recv()
			if err != nil {
				return err
			}
			if message.GetObservations() != nil {
				return stream.Send(&pb.ServerMessage{Payload: &pb.ServerMessage_Ack{Ack: &pb.BatchAck{AcknowledgedSequence: 1}}})
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state := &runtimeState{coverageSupported: true}
	if err := runSession(ctx, Config{ServerAddress: address}, wal, state, make(chan struct{})); err == nil {
		t.Fatal("invalid ACK was accepted")
	}
	if pending, _, _, acked, _ := wal.Stats(); pending != 2 || acked != 0 {
		t.Fatal("invalid ACK discarded pending records")
	}
	if state.supportsCoverage() {
		t.Fatal("legacy handshake did not disable sparse reporting")
	}
}

func TestWALRecoveryIsIndependentOfHistoricalGap(t *testing.T) {
	wal, err := OpenWAL(filepath.Join(t.TempDir(), "agent.wal"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	if err := wal.RecordGap(12, "capacity reached earlier"); err != nil {
		t.Fatal(err)
	}
	state := &runtimeState{}
	statuses := map[string]string{}
	for _, item := range state.report(wal) {
		statuses[item.Name] = item.Status
	}
	if statuses["local_wal"] != "supported" || statuses["data_gap"] != "degraded" {
		t.Fatalf("recovered WAL and historical gap were conflated: %v", statuses)
	}
	if _, err := wal.AppendBatch([]*pb.FlowObservation{testObservation("old")}); err != nil {
		t.Fatal(err)
	}
	wal.pending[0].ObservedAtUnixNanos = time.Now().Add(-25 * time.Hour).UnixNano()
	for _, item := range state.report(wal) {
		if item.Name == "local_wal" && item.Status != "degraded" {
			t.Fatal("aged backlog was not degraded")
		}
	}
	if _, err := wal.AppendBatch([]*pb.FlowObservation{testObservation("new")}); !errors.Is(err, ErrWALTooOld) {
		t.Fatal("age limit changed")
	}
}
