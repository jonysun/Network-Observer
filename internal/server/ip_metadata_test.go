package server

import (
	"io"
	"strings"
	"testing"
)

func TestDecodeIPMetadataPatchDistinguishesOmittedAndNull(t *testing.T) {
	patch, err := decodeIPMetadataPatch(io.NopCloser(strings.NewReader(`{"ip":"2001:db8::1","trusted":true,"city":null}`)))
	if err != nil {
		t.Fatal(err)
	}
	if patch.IP != "2001:db8::1" || patch.Trusted == nil || !*patch.Trusted {
		t.Fatalf("unexpected parsed patch: %#v", patch)
	}
	if !patch.SetCity || patch.City != nil {
		t.Fatalf("explicit null must clear city: %#v", patch)
	}
	if patch.SetDomain || patch.SetCountry {
		t.Fatalf("omitted fields must remain unchanged: %#v", patch)
	}
}

func TestDecodeIPMetadataPatchRejectsUnknownAndInvalidFields(t *testing.T) {
	for _, body := range []string{
		`{"ip":"192.0.2.1","unexpected":true}`,
		`{"ip":"192.0.2.1","trusted":null}`,
		`{"ip":"192.0.2.1","asn":"64512"}`,
	} {
		if _, err := decodeIPMetadataPatch(io.NopCloser(strings.NewReader(body))); err == nil {
			t.Fatalf("invalid patch accepted: %s", body)
		}
	}
}
