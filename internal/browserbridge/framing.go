package browserbridge

import (
	"encoding/binary"
	"fmt"
	"io"
)

const maxMessageBytes = 64 << 10

func ReadFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.NativeEndian.Uint32(header[:])
	if size == 0 || size > maxMessageBytes {
		return nil, fmt.Errorf("bridge message size is invalid")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("read bridge message: %w", err)
	}
	return payload, nil
}

func WriteFrame(writer io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxMessageBytes {
		return fmt.Errorf("bridge message size is invalid")
	}
	var header [4]byte
	binary.NativeEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(writer, header[:]); err != nil {
		return fmt.Errorf("write bridge header: %w", err)
	}
	if err := writeAll(writer, payload); err != nil {
		return fmt.Errorf("write bridge message: %w", err)
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
