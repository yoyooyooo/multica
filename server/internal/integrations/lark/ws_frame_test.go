package lark

import (
	"bytes"
	"testing"
)

// TestFrameRoundTripPreservesAllFields ensures every set field on the
// outbound Frame survives marshal+unmarshal. Field-tag mismatches
// against the SDK's pbbp2.Frame would silently corrupt frames on the
// wire — this test plus the explicit field numbers in ws_frame.go are
// the only checks that catch that.
func TestFrameRoundTripPreservesAllFields(t *testing.T) {
	t.Parallel()
	in := &Frame{
		SeqID:           42,
		LogID:           99,
		Service:         7,
		Method:          FrameMethodData,
		Headers:         []FrameHeader{{Key: "type", Value: "event"}, {Key: "message_id", Value: "om-1"}},
		PayloadEncoding: "json",
		PayloadType:     "im.message.receive_v1",
		Payload:         []byte(`{"schema":"2.0"}`),
		LogIDNew:        "log-new",
	}
	out, err := UnmarshalFrame(in.Marshal())
	if err != nil {
		t.Fatalf("UnmarshalFrame: %v", err)
	}
	if out.SeqID != in.SeqID || out.LogID != in.LogID || out.Service != in.Service || out.Method != in.Method {
		t.Errorf("scalar fields differ: in=%+v out=%+v", in, out)
	}
	if len(out.Headers) != len(in.Headers) {
		t.Fatalf("Headers len = %d; want %d", len(out.Headers), len(in.Headers))
	}
	for i, h := range out.Headers {
		if h != in.Headers[i] {
			t.Errorf("Headers[%d] = %+v; want %+v", i, h, in.Headers[i])
		}
	}
	if out.PayloadEncoding != in.PayloadEncoding {
		t.Errorf("PayloadEncoding = %q; want %q", out.PayloadEncoding, in.PayloadEncoding)
	}
	if out.PayloadType != in.PayloadType {
		t.Errorf("PayloadType = %q; want %q", out.PayloadType, in.PayloadType)
	}
	if !bytes.Equal(out.Payload, in.Payload) {
		t.Errorf("Payload = %q; want %q", string(out.Payload), string(in.Payload))
	}
	if out.LogIDNew != in.LogIDNew {
		t.Errorf("LogIDNew = %q; want %q", out.LogIDNew, in.LogIDNew)
	}
}

func TestFrameOmitsZeroFields(t *testing.T) {
	t.Parallel()
	// A minimal ping frame: Method=Control(0), Service=7, single
	// header. Method=0 is proto3 default and skipped by the
	// marshaller, so the encoded bytes contain only Service + Headers.
	ping := NewPingFrame(7)
	raw := ping.Marshal()
	if len(raw) == 0 {
		t.Fatal("ping marshal produced empty buffer")
	}
	out, err := UnmarshalFrame(raw)
	if err != nil {
		t.Fatalf("UnmarshalFrame: %v", err)
	}
	if out.Method != FrameMethodControl {
		t.Errorf("Method = %d; want Control", out.Method)
	}
	if out.Service != 7 {
		t.Errorf("Service = %d; want 7", out.Service)
	}
	if v := out.HeaderValue(FrameHeaderTypeKey); v != FrameHeaderTypePing {
		t.Errorf("type header = %q; want ping", v)
	}
	if out.SeqID != 0 || out.LogID != 0 || len(out.Payload) != 0 {
		t.Errorf("unset fields populated: %+v", out)
	}
}

func TestNewAckFrameReusesInboundHeaders(t *testing.T) {
	t.Parallel()
	inbound := &Frame{
		Method:  FrameMethodData,
		Service: 7,
		Headers: []FrameHeader{
			{Key: FrameHeaderTypeKey, Value: FrameHeaderTypeEvent},
			{Key: FrameHeaderMessageIDKey, Value: "om-42"},
		},
	}
	ack := NewAckFrame(inbound, true)
	if ack.Method != inbound.Method || ack.Service != inbound.Service {
		t.Errorf("ack method/service mismatch")
	}
	if len(ack.Headers) != len(inbound.Headers) {
		t.Errorf("ack headers length mismatch")
	}
	if ack.HeaderValue(FrameHeaderMessageIDKey) != "om-42" {
		t.Errorf("ack should echo message_id; got %q", ack.HeaderValue(FrameHeaderMessageIDKey))
	}
	if !contains(string(ack.Payload), `"code":200`) {
		t.Errorf("ack payload missing code=200: %s", string(ack.Payload))
	}

	nack := NewAckFrame(inbound, false)
	if !contains(string(nack.Payload), `"code":500`) {
		t.Errorf("nack payload missing code=500: %s", string(nack.Payload))
	}
}

func TestUnmarshalFrameRejectsTruncatedBuffer(t *testing.T) {
	t.Parallel()
	if _, err := UnmarshalFrame(nil); err == nil {
		t.Error("expected error on empty buffer")
	}
	// Tag byte for field 1 (varint) with no following varint payload.
	if _, err := UnmarshalFrame([]byte{0x08}); err == nil {
		t.Error("expected error on truncated varint")
	}
}

func TestUnmarshalFrameSkipsUnknownFields(t *testing.T) {
	t.Parallel()
	// Construct a buffer with one known field (Service=3, value=5)
	// and one unknown field (number 31, varint=99). The unknown
	// field MUST be skipped, not rejected.
	buf := []byte{}
	// field 3 (varint): tag = 3<<3|0 = 0x18, value = 5
	buf = append(buf, 0x18, 0x05)
	// field 31 (varint): tag = 31<<3|0 = 0xF8 0x01, value = 99 (0x63)
	buf = append(buf, 0xF8, 0x01, 0x63)
	f, err := UnmarshalFrame(buf)
	if err != nil {
		t.Fatalf("expected unknown field to be skipped, got error: %v", err)
	}
	if f.Service != 5 {
		t.Errorf("Service = %d; want 5", f.Service)
	}
}
