package common

import (
	"bytes"
	"compress/flate"
	"io"
)

// CompressionEnabled is a flag that can be passed at build time to disable compression
var CompressionEnabled string = "true"

func CompressData(data []byte) ([]byte, error) {
	if CompressionEnabled != "true" {
		return data, nil
	}

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
	if CompressionEnabled != "true" {
		return data, nil
	}

	reader := flate.NewReader(bytes.NewReader(data))
	defer reader.Close()
	decompressedData, err := io.ReadAll(reader)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return decompressedData, nil
}
