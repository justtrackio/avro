/*
Package container implements encoding and decoding of Avro Object Container Files as defined by the Avro specification.

See the Avro specification for an understanding of Avro: http://avro.apache.org/docs/current/

*/
package container

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/hamba/avro"
	"github.com/hamba/avro/internal/bytesx"
)

const (
	schemaKey = "avro.schema"
	codecKey  = "avro.codec"
)

var magicBytes = [4]byte{'O', 'b', 'j', 1}

// HeaderSchema is the Avro schema of a container file header.
var HeaderSchema = avro.MustParse(`{
	"type": "record", 
	"name": "org.apache.avro.file.Header",
	"fields": [
		{"name": "magic", "type": {"type": "fixed", "name": "Magic", "size": 4}},
		{"name": "meta", "type": {"type": "map", "values": "bytes"}},
		{"name": "sync", "type": {"type": "fixed", "name": "Sync", "size": 16}}
	]
}`)

// Header represents an Avro container file header.
type Header struct {
	Magic [4]byte           `avro:"magic"`
	Meta  map[string][]byte `avro:"meta"`
	Sync  [16]byte          `avro:"sync"`
}

// Decoder reads and decodes Avro values from a container file.
type Decoder struct {
	reader      *avro.Reader
	resetReader *bytesx.ResetReader
	decoder     *avro.Decoder
	sync        [16]byte

	count int64
}

// NewDecoder returns a new decoder that reads from reader r.
func NewDecoder(r io.Reader) (*Decoder, error) {
	reader := avro.NewReader(r, 1024)

	var h Header
	reader.ReadVal(HeaderSchema, &h)
	if reader.Error != nil {
		return nil, fmt.Errorf("decoder: unexpected error: %v", reader.Error)
	}

	if h.Magic != magicBytes {
		return nil, errors.New("decoder: invalid avro file")
	}
	schema, err := avro.Parse(string(h.Meta[schemaKey]))
	if err != nil {
		return nil, err
	}

	decReader := bytesx.NewResetReader([]byte{})

	// TODO: File Codecs
	// codec, ok := codecs[string(h.Meta[codecKey])]
	//if codec, ok := codecs[string(h.Meta[codecKey])]; !ok {
	//	return nil, fmt.Errorf("file: unknown codec %s", string(h.Meta[codecKey]))
	//}

	return &Decoder{
		reader:      reader,
		resetReader: decReader,
		decoder:     avro.NewDecoderForSchema(schema, decReader),
		sync:        h.Sync,
	}, nil
}

// HasNext determines if there is another value to read.
func (d *Decoder) HasNext() bool {
	if d.count <= 0 {
		count := d.readBlock()
		d.count = count
	}

	return d.count > 0
}

// Decode reads the next Avro encoded value from its input and stores it in the value pointed to by v.
func (d *Decoder) Decode(v interface{}) error {
	if d.count <= 0 {
		return errors.New("decoder: no data found, call HasNext first")
	}

	d.count--

	return d.decoder.Decode(v)
}

// Error returns the last reader error.
func (d *Decoder) Error() error {
	if d.reader.Error == io.EOF {
		return nil
	}

	return d.reader.Error
}

func (d *Decoder) readBlock() int64 {
	count := d.reader.ReadLong()
	size := d.reader.ReadLong()

	data := make([]byte, size)
	d.reader.Read(data)
	d.resetReader.Reset(data)

	var sync [16]byte
	d.reader.Read(sync[:])
	if d.sync != sync && d.reader.Error != io.EOF {
		d.reader.Error = errors.New("decoder: invalid block")
	}

	return count
}

// EncoderFunc represents an configuration function for Encoder
type EncoderFunc func(e *Encoder)

// WithBlockLength sets the block length on the encoder.
func WithBlockLength(length int) EncoderFunc {
	return func(e *Encoder) {
		e.blockLength = length
	}
}

// Encoder writes Avro container file to an output stream.
type Encoder struct {
	writer  *avro.Writer
	buf     *bytes.Buffer
	encoder *avro.Encoder
	sync    [16]byte

	blockLength int
	count       int
}

// NewEncoder returns a new encoder that writes to w using schema s.
func NewEncoder(s string, w io.Writer, opts ...EncoderFunc) (*Encoder, error) {
	schema, err := avro.Parse(s)
	if err != nil {
		return nil, err
	}

	writer := avro.NewWriter(w, 512)

	header := Header{
		Magic: magicBytes,
		Meta: map[string][]byte{
			schemaKey: []byte(schema.String()),
		},
	}
	_, _ = rand.Read(header.Sync[:])
	writer.WriteVal(HeaderSchema, header)

	buf := &bytes.Buffer{}

	e := &Encoder{
		writer:      writer,
		buf:         buf,
		encoder:     avro.NewEncoderForSchema(schema, buf),
		sync:        header.Sync,
		blockLength: 100,
	}

	for _, opt := range opts {
		opt(e)
	}

	return e, nil
}

// Encode writes the Avro encoding of v to the stream.
func (e *Encoder) Encode(v interface{}) error {
	if err := e.encoder.Encode(v); err != nil {
		return err
	}

	e.count++
	if e.count >= e.blockLength {
		if err := e.writerBlock(); err != nil {
			return err
		}
	}

	return e.writer.Error
}

// Close closes the encoder, flushing the writer.
func (e *Encoder) Close() error {
	if e.count == 0 {
		return nil
	}

	if err := e.writerBlock(); err != nil {
		return err
	}

	return e.writer.Error
}

func (e *Encoder) writerBlock() error {
	e.writer.WriteLong(int64(e.count))
	e.writer.WriteLong(int64(e.buf.Len()))
	e.writer.Write(e.buf.Bytes())
	e.writer.Write(e.sync[:])
	e.count = 0
	e.buf.Reset()
	return e.writer.Flush()
}