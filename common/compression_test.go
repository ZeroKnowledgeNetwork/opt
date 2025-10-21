package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompressData(t *testing.T) {
	data := []byte("test data")
	compressedData, err := CompressData(data)
	require.NoError(t, err)
	require.NotEqual(t, data, compressedData)

	decompressedData, err := DecompressData(compressedData)
	require.NoError(t, err)
	require.Equal(t, data, decompressedData)
}
