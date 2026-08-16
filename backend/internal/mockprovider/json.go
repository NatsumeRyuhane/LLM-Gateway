package mockprovider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func bytesReader(value []byte) *bytes.Reader { return bytes.NewReader(value) }

func requireEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("contains trailing JSON values")
}
