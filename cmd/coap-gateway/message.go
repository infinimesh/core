package main

import (
	"encoding/binary"
	"errors"
)

const (
	codePost   = 0x02
	optURIPath = 11
)

type coapMessage struct {
	Type      byte
	Code      byte
	MessageID uint16
	Token     []byte
	Path      []string
	Payload   []byte
}

func parseCoAP(b []byte) (coapMessage, error) {
	if len(b) < 4 {
		return coapMessage{}, errors.New("short")
	}
	if b[0]>>6 != 1 {
		return coapMessage{}, errors.New("version")
	}
	tkl := int(b[0] & 0x0f)
	if tkl > 8 || len(b) < 4+tkl {
		return coapMessage{}, errors.New("token")
	}
	msg := coapMessage{Type: b[0] >> 4 & 0x03, Code: b[1], MessageID: binary.BigEndian.Uint16(b[2:4]), Token: append([]byte(nil), b[4:4+tkl]...)}
	off := 4 + tkl
	var delta int
	for off < len(b) {
		if b[off] == 0xff {
			msg.Payload = append([]byte(nil), b[off+1:]...)
			break
		}
		d, n, err := optNibble(b[off] >> 4)
		if err != nil {
			return coapMessage{}, err
		}
		l, m, err := optNibble(b[off] & 0x0f)
		if err != nil {
			return coapMessage{}, err
		}
		off++
		if n > 0 {
			if off+n > len(b) {
				return coapMessage{}, errors.New("option")
			}
			d = int(readExt(b[off:off+n], n))
			off += n
		}
		if m > 0 {
			if off+m > len(b) {
				return coapMessage{}, errors.New("option")
			}
			l = int(readExt(b[off:off+m], m))
			off += m
		}
		if off+l > len(b) {
			return coapMessage{}, errors.New("option")
		}
		delta += d
		val := b[off : off+l]
		off += l
		if delta == optURIPath {
			msg.Path = append(msg.Path, string(val))
		}
	}
	return msg, nil
}

func optNibble(v byte) (int, int, error) {
	switch v {
	case 13:
		return 0, 1, nil
	case 14:
		return 0, 2, nil
	case 15:
		return 0, 0, errors.New("option")
	default:
		return int(v), 0, nil
	}
}

func readExt(b []byte, n int) uint {
	if n == 1 {
		return uint(b[0]) + 13
	}
	return uint(binary.BigEndian.Uint16(b)) + 269
}

func ack(req coapMessage, code byte, payload []byte) []byte {
	tkl := len(req.Token)
	out := []byte{0x60 | byte(tkl), code, byte(req.MessageID >> 8), byte(req.MessageID)}
	out = append(out, req.Token...)
	if len(payload) > 0 {
		out = append(out, 0xff)
		out = append(out, payload...)
	}
	return out
}

func reportedDevice(path []string) (string, bool) {
	if len(path) == 4 && path[0] == "devices" && path[2] == "state" && path[3] == "reported" && path[1] != "" {
		return path[1], true
	}
	return "", false
}
