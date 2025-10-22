package common

import (
	"bytes"
	"compress/flate"
	"io"
)

func CompressData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	_, err = writer.Write(data)
	if err != nil {
		return nil, err
	}
	err = writer.Close()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecompressData(data []byte) ([]byte, error) {
	reader := flate.NewReader(bytes.NewReader(data))
	defer reader.Close()
	decompressedData, err := io.ReadAll(reader)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return decompressedData, nil
}
