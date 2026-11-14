package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type runtimeState struct {
	mu           sync.Mutex
	capabilities []*pb.Capability
}

func (s *runtimeState) set(items []*pb.Capability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.capabilities = items
}

func (s *runtimeState) report(wal *WAL) []*pb.Capability {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*pb.Capability, 0, len(s.capabilities)+2)
	for _, item := range s.capabilities {
		if item != nil {
			items = append(items, proto.Clone(item).(*pb.Capability))
		}
	}
	pending, used, _, _, _ := wal.Stats()
	gap := wal.GapStatus()
	state := "supported"
	if gap.Dropped != 0 || !gap.First.IsZero() {
		state = "degraded"
	}
	items = append(items, &pb.Capability{Name: "local_wal", Status: state,
		Detail:             fmt.Sprintf("24h buffering limit; %d pending; %d/%d bytes; old unacknowledged records are preserved", pending, used, wal.maxBytes),
		CheckedAtUnixNanos: time.Now().UnixNano()})
	if !gap.First.IsZero() {
		items = append(items, &pb.Capability{Name: "data_gap", Status: "degraded",
			Detail:             fmt.Sprintf("%d observations skipped; first=%s; last=%s; reason=%s", gap.Dropped, gap.First.Format(time.RFC3339), gap.Last.Format(time.RFC3339), gap.Reason),
			CheckedAtUnixNanos: time.Now().UnixNano()})
	}
	return items
}

func Run(ctx context.Context, configPath string, logger *slog.Logger) error {
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if logger == nil {
		logger = slog.Default()
	}
	wal, err := OpenWAL(config.WALPath, config.WALMaxBytes)
	if err != nil {
		return err
	}
	defer wal.Close()
	return run(ctx, config, wal, newConfiguredCollector(config), logger)
}

func run(ctx context.Context, config Config, wal *WAL, collector Collector, logger *slog.Logger) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &runtimeState{}
	changed := make(chan struct{}, 1)
	collectionResult := make(chan error, 1)
	collectionDone := make(chan struct{})
	go func() {
		defer close(collectionDone)
		collectionResult <- collectLoop(runCtx, config, wal, collector, state, changed, logger)
		cancel()
	}()
	defer func() {
		cancel()
		<-collectionDone
		if closer, ok := collector.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				logger.Warn("close collector resources", "error", err)
			}
		}
	}()
	backoff := time.Second
	for runCtx.Err() == nil {
		started := time.Now()
		err := runSession(runCtx, config, wal, state, changed)
		if status.Code(err) == codes.Unauthenticated {
			return fmt.Errorf("device credential rejected; agent stopped: %w", err)
		}
		if runCtx.Err() != nil {
			break
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		logger.Warn("agent stream disconnected", "error", err, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-runCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
		backoff = min(30*time.Second, backoff*2)
	}
	select {
	case err := <-collectionResult:
		return err
	default:
		return nil
	}
}

func collectLoop(ctx context.Context, config Config, wal *WAL, collector Collector, state *runtimeState, changed chan<- struct{}, logger *slog.Logger) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		observations, capabilities, err := collector.Snapshot(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Warn("endpoint snapshot failed", "error", err)
			capabilities = append(capabilities, &pb.Capability{Name: "endpoint_collection", Status: "degraded", Detail: err.Error(), CheckedAtUnixNanos: time.Now().UnixNano()})
			if err := wal.RecordGap(0, "endpoint snapshot failed"); err != nil {
				return err
			}
		} else {
			for _, observation := range observations {
				if observation == nil {
					return errors.New("collector returned a nil observation")
				}
				observation.DeviceId = config.DeviceID
				observation.ObserverId = config.DeviceID + ":" + config.ObserverType
				observation.ObserverType = config.ObserverType
			}
			appended, err := wal.AppendBatch(observations)
			if errors.Is(err, ErrWALFull) || errors.Is(err, ErrWALTooOld) {
				if gapErr := wal.RecordGap(uint64(len(observations)-appended), err.Error()); gapErr != nil {
					return gapErr
				}
			} else if err != nil {
				return err
			}
		}
		state.set(capabilities)
		select {
		case changed <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func runSession(ctx context.Context, config Config, wal *WAL, state *runtimeState, changed <-chan struct{}) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	connection, err := dial(ctx, config.ServerAddress, config.CACertFile, config.SecurityMode)
	if err != nil {
		return err
	}
	defer connection.Close()
	streamContext := metadata.AppendToOutgoingContext(ctx, "x-network-observer-security-mode", strconv.FormatBool(config.SecurityMode))
	stream, err := pb.NewAgentServiceClient(connection).Connect(streamContext)
	if err != nil {
		return fmt.Errorf("open agent stream: %w", err)
	}
	_, _, _, lastAcked, _ := wal.Stats()
	if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Authenticate{Authenticate: &pb.AuthenticateRequest{
		DeviceId: config.DeviceID, DeviceCredential: config.EnrollmentToken, LastAcknowledgedSequence: lastAcked,
	}}}); err != nil {
		return err
	}
	response, err := receiveWithin(ctx, cancel, stream)
	if err != nil {
		return err
	}
	authenticated := response.GetAuthenticated()
	if authenticated == nil {
		return errors.New("server did not authenticate agent stream")
	}
	if err := wal.Reconcile(authenticated.NextSequence); err != nil {
		return err
	}
	heartbeatInterval := time.Duration(authenticated.HeartbeatIntervalSeconds) * time.Second
	if heartbeatInterval < time.Second || heartbeatInterval > time.Minute {
		heartbeatInterval = 15 * time.Second
	}
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	lastDigest := [32]byte{}
	for {
		capabilities := state.report(wal)
		if digest := capabilityDigest(capabilities); digest != lastDigest {
			if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Capabilities{Capabilities: &pb.CapabilityReport{Capabilities: capabilities}}}); err != nil {
				return err
			}
			lastDigest = digest
		}
		// One bounded batch per iteration keeps heartbeat/status updates responsive during replay.
		batch := wal.Pending(500)
		if len(batch) != 0 {
			first, last := batch[0].Sequence, batch[len(batch)-1].Sequence
			if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Observations{Observations: &pb.ObservationBatch{
				Observations: batch, FirstSequence: first, LastSequence: last,
			}}}); err != nil {
				return err
			}
			response, err := receiveUntilAck(ctx, cancel, stream)
			if err != nil {
				return err
			}
			ack := response.GetAck()
			if ack == nil || ack.AcknowledgedSequence != last {
				return fmt.Errorf("invalid acknowledgement for batch %d-%d", first, last)
			}
			if err := wal.AckThrough(last); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat.C:
			_, _, _, _, sequence := wal.Stats()
			if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Heartbeat{Heartbeat: &pb.Heartbeat{
				Sequence: sequence, SentAtUnixNanos: time.Now().UnixNano(),
			}}}); err != nil {
				return err
			}
		case <-changed:
		case <-poll.C:
		}
	}
}

func receiveUntilAck(ctx context.Context, cancel context.CancelFunc, stream grpc.BidiStreamingClient[pb.AgentMessage, pb.ServerMessage]) (*pb.ServerMessage, error) {
	for {
		response, err := receiveWithin(ctx, cancel, stream)
		if err != nil {
			return nil, err
		}
		if response.GetAck() != nil {
			return response, nil
		}
		if message := response.GetError(); message != nil {
			if message.Retryable {
				return nil, status.Error(codes.Unavailable, message.Message)
			}
			return nil, status.Error(codes.InvalidArgument, message.Message)
		}
		// Config updates and future server messages do not acknowledge observations.
	}
}

func sendWithin(ctx context.Context, cancel context.CancelFunc, stream grpc.BidiStreamingClient[pb.AgentMessage, pb.ServerMessage], message *pb.AgentMessage) error {
	result := make(chan error, 1)
	go func() { result <- stream.Send(message) }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		cancel()
		return errors.New("agent send timed out")
	}
}

func receiveWithin(ctx context.Context, cancel context.CancelFunc, stream grpc.BidiStreamingClient[pb.AgentMessage, pb.ServerMessage]) (*pb.ServerMessage, error) {
	type response struct {
		message *pb.ServerMessage
		err     error
	}
	result := make(chan response, 1)
	go func() { message, err := stream.Recv(); result <- response{message, err} }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case result := <-result:
		return result.message, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		return nil, errors.New("agent acknowledgement timed out")
	}
}
