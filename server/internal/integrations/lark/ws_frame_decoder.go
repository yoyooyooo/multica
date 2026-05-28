package lark

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// LarkJSONFrameDecoder decodes the JSON event payload Lark nests
// inside a long-conn data Frame. The outer binary Frame envelope
// (ws_frame.go) is stripped by the connector; the decoder only sees
// the bytes from Frame.Payload, which Lark formats as the standard
// event-subscription envelope: {schema, header, event}.
//
// Three outcomes:
//
//   - (msg, true,  nil) — `im.message.receive_v1` event. The Hub
//     forwards through the Dispatcher.
//   - (zero, false, nil) — heartbeat-shaped JSON or an event_type we
//     don't yet handle (im.chat.access_event_v1, etc.). The connector
//     drops these silently and still sends a 200 ACK to Lark so the
//     server stops resending.
//   - (zero, false, err) — malformed JSON or schema we couldn't
//     parse. The connector logs + drops the single frame; the WS
//     connection stays up because one bad payload shouldn't amplify
//     into a reconnect storm.
//
// The decoder is stateless and goroutine-safe — a single instance
// serves every supervisor goroutine.
type LarkJSONFrameDecoder struct{}

func NewLarkJSONFrameDecoder() *LarkJSONFrameDecoder { return &LarkJSONFrameDecoder{} }

// Decode implements FrameDecoder.
func (d *LarkJSONFrameDecoder) Decode(payload []byte, inst db.LarkInstallation) (InboundMessage, bool, error) {
	if len(payload) == 0 {
		return InboundMessage{}, false, nil
	}
	var env larkEventEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return InboundMessage{}, false, fmt.Errorf("envelope: %w", err)
	}

	// Lark long-conn data frames are always v2 event envelopes
	// (schema "2.0"). The legacy webhook v1 "type":"event_callback"
	// shape is not used on long-conn — we accept it defensively in
	// case Lark adds a back-compat mode, but the canonical path is
	// schema-driven.
	if env.Type != "" && env.Type != "event_callback" {
		return InboundMessage{}, false, nil
	}

	if env.Header.EventType != "im.message.receive_v1" {
		return InboundMessage{}, false, nil
	}

	if env.Event == nil {
		return InboundMessage{}, false, errors.New("event_callback with empty event payload")
	}
	var evt larkMessageReceiveEvent
	if err := json.Unmarshal(env.Event, &evt); err != nil {
		return InboundMessage{}, false, fmt.Errorf("event: %w", err)
	}

	msg := InboundMessage{
		EventType:    env.Header.EventType,
		EventID:      env.Header.EventID,
		AppID:        env.Header.AppID,
		ChatID:       ChatID(evt.Message.ChatID),
		ChatType:     normalizeChatType(evt.Message.ChatType),
		MessageID:    evt.Message.MessageID,
		SenderOpenID: OpenID(evt.Sender.SenderID.OpenID),
	}

	switch evt.Message.MessageType {
	case "text":
		msg.Body = extractTextBody(evt.Message.Content)
	}

	if msg.ChatType == ChatTypeGroup {
		msg.AddressedToBot = containsMention(evt.Message.Mentions, inst.BotOpenID)
	}

	return msg, true, nil
}

// larkEventEnvelope mirrors the outer JSON Lark wraps every push in.
type larkEventEnvelope struct {
	Schema string          `json:"schema"`
	Type   string          `json:"type"`
	Header larkEventHeader `json:"header"`
	Event  json.RawMessage `json:"event"`
}

type larkEventHeader struct {
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	CreateTime string `json:"create_time"`
	AppID      string `json:"app_id"`
	TenantKey  string `json:"tenant_key"`
}

// larkMessageReceiveEvent is the documented payload of
// im.message.receive_v1.
type larkMessageReceiveEvent struct {
	Sender struct {
		SenderID struct {
			OpenID  string `json:"open_id"`
			UnionID string `json:"union_id"`
			UserID  string `json:"user_id"`
		} `json:"sender_id"`
		SenderType string `json:"sender_type"`
		TenantKey  string `json:"tenant_key"`
	} `json:"sender"`
	Message struct {
		MessageID   string        `json:"message_id"`
		ChatID      string        `json:"chat_id"`
		ChatType    string        `json:"chat_type"`
		MessageType string        `json:"message_type"`
		Content     string        `json:"content"`
		Mentions    []larkMention `json:"mentions"`
		CreateTime  string        `json:"create_time"`
	} `json:"message"`
}

type larkMention struct {
	Key string `json:"key"`
	ID  struct {
		OpenID  string `json:"open_id"`
		UnionID string `json:"union_id"`
		UserID  string `json:"user_id"`
	} `json:"id"`
	Name string `json:"name"`
}

func extractTextBody(content string) string {
	if content == "" {
		return ""
	}
	var doc struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		return ""
	}
	return doc.Text
}

func normalizeChatType(t string) ChatType {
	switch strings.ToLower(t) {
	case "p2p":
		return ChatTypeP2P
	case "group":
		return ChatTypeGroup
	default:
		return ChatType(t)
	}
}

func containsMention(mentions []larkMention, botOpenID string) bool {
	if botOpenID == "" {
		return false
	}
	for _, m := range mentions {
		if m.ID.OpenID == botOpenID {
			return true
		}
	}
	return false
}
