package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

// CanonicalJSON encodes v as DESIGN-024 section 4 does: sorted keys, no extra
// whitespace and no HTML escaping, numbers kept as written. Its bytes are
// those of encoding v, decoding that into generic values with UseNumber and
// encoding them again, as it was built through 2.26; a test holds the two
// equal. It rewrites v's compact encoding in place of building the generic
// tree, whose maps and interfaces cost several times the JSON's size: the
// operator holds a history plan of tens of thousands of files in 256 MiB
// (DESIGN-025 5.4). It holds v's encoding and the output, and nothing else
// that grows with them.
func CanonicalJSON(v any) ([]byte, error) {
	in, err := encodeCompact(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(in))
	if err := writeCanonical(&out, in); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CanonicalEqual reports whether CanonicalJSON(v) is want, without building
// the canonical bytes: it compares them with want as they are written.
func CanonicalEqual(v any, want []byte) (bool, error) {
	in, err := encodeCompact(v)
	if err != nil {
		return false, err
	}
	cmp := &compareSink{want: want}
	switch err := writeCanonical(cmp, in); {
	case errors.Is(err, errCanonicalDiffers):
		return false, nil
	case err != nil:
		return false, err
	}
	return cmp.n == len(want), nil
}

// encodeCompact is v's encoding without HTML escaping. encoding/json writes
// it compact, json.RawMessage included, and valid.
func encodeCompact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// canonicalSink is where writeCanonical writes: a bytes.Buffer, or a
// compareSink.
type canonicalSink interface {
	io.Writer
	io.ByteWriter
	io.StringWriter
}

var errCanonicalJSON = errors.New("canonical JSON: the encoding is not compact JSON")

// writeCanonical writes the canonical form of in, one compact JSON value.
func writeCanonical(w canonicalSink, in []byte) error {
	c := canonicalWriter{in: in, w: w}
	c.str = json.NewEncoder(&c.scratch)
	c.str.SetEscapeHTML(false)
	end, err := c.value(0)
	if err != nil {
		return err
	}
	if end != len(in) {
		return errCanonicalJSON
	}
	return nil
}

type canonicalWriter struct {
	in []byte
	w  canonicalSink
	// str encodes a string as encoding/json does without HTML escaping,
	// into scratch.
	str     *json.Encoder
	scratch bytes.Buffer
}

// at is in[i], or 0 past its end.
func (c *canonicalWriter) at(i int) byte {
	if i < 0 || i >= len(c.in) {
		return 0
	}
	return c.in[i]
}

// value writes the value that starts at in[i] and returns where it ends.
func (c *canonicalWriter) value(i int) (int, error) {
	switch c.at(i) {
	case '{':
		return c.object(i)
	case '[':
		return c.array(i)
	case '"':
		end, err := skipJSONString(c.in, i)
		if err != nil {
			return 0, err
		}
		var s string
		if err := json.Unmarshal(c.in[i:end], &s); err != nil {
			return 0, err
		}
		return end, c.string(s)
	case 0:
		return 0, errCanonicalJSON
	default:
		// A number, true, false or null: as written, as json.Number and the
		// generic literals encode them.
		end := skipJSONScalar(c.in, i)
		if end == i {
			return 0, errCanonicalJSON
		}
		_, err := c.w.Write(c.in[i:end])
		return end, err
	}
}

type canonicalMember struct {
	key        string
	start, end int
}

// object writes the object at in[i] with its keys sorted as encoding/json
// sorts a map's (strings.Compare). A key that occurs twice keeps its last
// value, as a map assignment does.
func (c *canonicalWriter) object(i int) (int, error) {
	var members []canonicalMember
	j := i + 1
	end := 0
	if c.at(j) == '}' {
		end = j + 1
	} else {
		for {
			ke, err := skipJSONString(c.in, j)
			if err != nil {
				return 0, err
			}
			var key string
			if err := json.Unmarshal(c.in[j:ke], &key); err != nil {
				return 0, err
			}
			if c.at(ke) != ':' {
				return 0, errCanonicalJSON
			}
			ve, err := skipJSONValue(c.in, ke+1)
			if err != nil {
				return 0, err
			}
			members = append(members, canonicalMember{key: key, start: ke + 1, end: ve})
			if c.at(ve) == ',' {
				j = ve + 1
				continue
			}
			if c.at(ve) != '}' {
				return 0, errCanonicalJSON
			}
			end = ve + 1
			break
		}
	}
	sort.SliceStable(members, func(a, b int) bool { return members[a].key < members[b].key })
	if err := c.w.WriteByte('{'); err != nil {
		return 0, err
	}
	first := true
	for k, m := range members {
		if k+1 < len(members) && members[k+1].key == m.key {
			continue
		}
		if !first {
			if err := c.w.WriteByte(','); err != nil {
				return 0, err
			}
		}
		first = false
		if err := c.string(m.key); err != nil {
			return 0, err
		}
		if err := c.w.WriteByte(':'); err != nil {
			return 0, err
		}
		if e, err := c.value(m.start); err != nil {
			return 0, err
		} else if e != m.end {
			return 0, errCanonicalJSON
		}
	}
	return end, c.w.WriteByte('}')
}

// array writes the array at in[i], its elements in order.
func (c *canonicalWriter) array(i int) (int, error) {
	if err := c.w.WriteByte('['); err != nil {
		return 0, err
	}
	j := i + 1
	if c.at(j) == ']' {
		return j + 1, c.w.WriteByte(']')
	}
	for n := 0; ; n++ {
		if n > 0 {
			if err := c.w.WriteByte(','); err != nil {
				return 0, err
			}
		}
		e, err := c.value(j)
		if err != nil {
			return 0, err
		}
		switch c.at(e) {
		case ',':
			j = e + 1
		case ']':
			return e + 1, c.w.WriteByte(']')
		default:
			return 0, errCanonicalJSON
		}
	}
}

// string writes s as encoding/json encodes a string without HTML escaping.
func (c *canonicalWriter) string(s string) error {
	c.scratch.Reset()
	if err := c.str.Encode(s); err != nil {
		return err
	}
	_, err := c.w.Write(bytes.TrimSuffix(c.scratch.Bytes(), []byte("\n")))
	return err
}

// skipJSONString returns the index just past the string literal at in[i].
func skipJSONString(in []byte, i int) (int, error) {
	if i >= len(in) || in[i] != '"' {
		return 0, errCanonicalJSON
	}
	for j := i + 1; j < len(in); j++ {
		switch in[j] {
		case '\\':
			j++
		case '"':
			return j + 1, nil
		}
	}
	return 0, errCanonicalJSON
}

// skipJSONScalar returns the index just past the number or literal at in[i].
func skipJSONScalar(in []byte, i int) int {
	j := i
	for j < len(in) {
		switch in[j] {
		case ',', ']', '}', ':', '"', '[', '{':
			return j
		}
		j++
	}
	return j
}

// skipJSONValue returns the index just past the value at in[i].
func skipJSONValue(in []byte, i int) (int, error) {
	if i >= len(in) {
		return 0, errCanonicalJSON
	}
	switch in[i] {
	case '"':
		return skipJSONString(in, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(in); j++ {
			switch in[j] {
			case '"':
				e, err := skipJSONString(in, j)
				if err != nil {
					return 0, err
				}
				j = e - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, nil
				}
			}
		}
		return 0, errCanonicalJSON
	default:
		if e := skipJSONScalar(in, i); e > i {
			return e, nil
		}
		return 0, errCanonicalJSON
	}
}

var errCanonicalDiffers = errors.New("the canonical bytes differ")

// compareSink compares what is written with want, and stops at the first
// byte that differs.
type compareSink struct {
	want []byte
	n    int
}

func (s *compareSink) Write(p []byte) (int, error) {
	if s.n+len(p) > len(s.want) || !bytes.Equal(s.want[s.n:s.n+len(p)], p) {
		return 0, errCanonicalDiffers
	}
	s.n += len(p)
	return len(p), nil
}

func (s *compareSink) WriteByte(b byte) error {
	if s.n >= len(s.want) || s.want[s.n] != b {
		return errCanonicalDiffers
	}
	s.n++
	return nil
}

func (s *compareSink) WriteString(str string) (int, error) {
	if s.n+len(str) > len(s.want) || string(s.want[s.n:s.n+len(str)]) != str {
		return 0, errCanonicalDiffers
	}
	s.n += len(str)
	return len(str), nil
}
