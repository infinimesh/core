/*
Copyright © 2021-2026 Infinite Devices GmbH

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package packet

import (
	"encoding/binary"
	"errors"
	"io"
)

var ErrPacketTooLarge = errors.New("packet exceeds maximum size")

func readRemaining(r io.Reader) (value int, width int, err error) {
	mul := 1
	for i := 0; i < 4; i++ {
		b := make([]byte, 1)
		if _, err = io.ReadFull(r, b); err != nil {
			return 0, width, err
		}
		width++
		value += int(b[0]&127) * mul
		if b[0]&128 == 0 {
			return value, width, nil
		}
		mul *= 128
	}
	return 0, width, errors.New("malformed remaining length")
}

func appendRemaining(dst []byte, n int) []byte {
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 128
		}
		dst = append(dst, b)
		if n == 0 {
			return dst
		}
	}
}

func writePacket(w io.Writer, first byte, body []byte) (int64, error) {
	buf := append([]byte{first}, appendRemaining(nil, len(body))...)
	buf = append(buf, body...)
	n, err := w.Write(buf)
	return int64(n), err
}

func readVarInt(b []byte, off int) (int, int, error) {
	mul := 1
	val := 0
	for i := 0; i < 4; i++ {
		if off >= len(b) {
			return 0, off, io.ErrUnexpectedEOF
		}
		c := b[off]
		off++
		val += int(c&127) * mul
		if c&128 == 0 {
			return val, off, nil
		}
		mul *= 128
	}
	return 0, off, errors.New("malformed variable integer")
}

func readString(b []byte, off int) (string, int, error) {
	raw, n, err := readBytes(b, off)
	return string(raw), n, err
}

func readBytes(b []byte, off int) ([]byte, int, error) {
	if off+2 > len(b) {
		return nil, off, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(b[off : off+2]))
	off += 2
	if off+n > len(b) {
		return nil, off, io.ErrUnexpectedEOF
	}
	return append([]byte(nil), b[off:off+n]...), off + n, nil
}

func appendString(dst []byte, s string) []byte {
	n := make([]byte, 2)
	binary.BigEndian.PutUint16(n, uint16(len(s)))
	dst = append(dst, n...)
	return append(dst, s...)
}

func appendBytes(dst []byte, raw []byte) []byte {
	n := make([]byte, 2)
	binary.BigEndian.PutUint16(n, uint16(len(raw)))
	dst = append(dst, n...)
	return append(dst, raw...)
}

func u16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
func u32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

func putU16(dst []byte, v uint16) []byte {
	n := make([]byte, 2)
	binary.BigEndian.PutUint16(n, v)
	return append(dst, n...)
}

func putU32(dst []byte, v uint32) []byte {
	n := make([]byte, 4)
	binary.BigEndian.PutUint32(n, v)
	return append(dst, n...)
}
