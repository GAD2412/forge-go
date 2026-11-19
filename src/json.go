package main

// JSON output in field order: an object is an O (ordered key/value pairs), a list is []any, and raw
// pre-encoded JSON is R. One encoder writes them all; every reply is built from these.

import (
	"strconv"
	"unicode/utf8"
)

type KV struct {
	K string
	V any
}
type O []KV
type R []byte

func (o O) get(k string) any {
	for _, kv := range o {
		if kv.K == k {
			return kv.V
		}
	}
	return nil
}

func enc(b []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(b, "null"...)
	case string:
		return appendStr(b, x)
	case int:
		return strconv.AppendInt(b, int64(x), 10)
	case int64:
		return strconv.AppendInt(b, x, 10)
	case float64:
		if x == float64(int64(x)) {
			return strconv.AppendInt(b, int64(x), 10)
		}
		return strconv.AppendFloat(b, x, 'g', -1, 64)
	case bool:
		if x {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case R:
		return append(b, x...)
	case O:
		b = append(b, '{')
		for i, kv := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendStr(b, kv.K)
			b = append(b, ':')
			b = enc(b, kv.V)
		}
		return append(b, '}')
	case []any:
		b = append(b, '[')
		for i, e := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = enc(b, e)
		}
		return append(b, ']')
	case []string:
		b = append(b, '[')
		for i, e := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendStr(b, e)
		}
		return append(b, ']')
	case map[string]any:
		// decoded input echoed back (tool arguments); key order is not significant there
		o := make(O, 0, len(x))
		for k, v := range x {
			o = append(o, KV{k, v})
		}
		return enc(b, o)
	}
	return append(b, "null"...)
}

func jstr(s string) []byte { return appendStr(nil, s) }

const hexDigits = "0123456789abcdef"

func appendStr(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c < utf8.RuneSelf {
			i++
			continue
		}
		if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				b = append(b, s[start:i]...)
				b = append(b, `�`...)
				i++
				start = i
				continue
			}
			i += size
			continue
		}
		b = append(b, s[start:i]...)
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&15])
		}
		i++
		start = i
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}
