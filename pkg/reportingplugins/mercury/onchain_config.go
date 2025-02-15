package mercury

import (
	"math/big"

	pkgerrors "github.com/pkg/errors"

	"github.com/smartcontractkit/libocr/bigbigendian"
)

const onchainConfigVersion = 1

var onchainConfigVersionBig = big.NewInt(onchainConfigVersion)

const onchainConfigEncodedLength = 96 // 3x 32bit evm words, version + min + max

type OnchainConfig struct {
	// applies to all values: price, bid and ask
	Min *big.Int
	Max *big.Int
}

var _ OnchainConfigCodec = StandardOnchainConfigCodec{}

// StandardOnchainConfigCodec provides a mercury-specific implementation of
// OnchainConfigCodec.
//
// An encoded onchain config is expected to be in the format
// <version><min><max>
// where version is a uint8 and min and max are in the format
// returned by EncodeValueInt192.
type StandardOnchainConfigCodec struct{}

func (StandardOnchainConfigCodec) Decode(b []byte) (OnchainConfig, error) {
	if len(b) != onchainConfigEncodedLength {
		return OnchainConfig{}, pkgerrors.Errorf("unexpected length of OnchainConfig, expected %v, got %v", onchainConfigEncodedLength, len(b))
	}

	v, err := bigbigendian.DeserializeSigned(32, b[:32])
	if err != nil {
		return OnchainConfig{}, err
	}
	if v.Cmp(onchainConfigVersionBig) != 0 {
		return OnchainConfig{}, pkgerrors.Errorf("unexpected version of OnchainConfig, expected %v, got %v", onchainConfigVersion, v)
	}

	min, err := bigbigendian.DeserializeSigned(32, b[32:64])
	if err != nil {
		return OnchainConfig{}, err
	}
	max, err := bigbigendian.DeserializeSigned(32, b[64:96])
	if err != nil {
		return OnchainConfig{}, err
	}

	if !(min.Cmp(max) <= 0) {
		return OnchainConfig{}, pkgerrors.Errorf("OnchainConfig min (%v) should not be greater than max(%v)", min, max)
	}

	return OnchainConfig{min, max}, nil
}

func (StandardOnchainConfigCodec) Encode(c OnchainConfig) ([]byte, error) {
	verBytes, err := bigbigendian.SerializeSigned(32, onchainConfigVersionBig)
	if err != nil {
		return nil, err
	}
	minBytes, err := bigbigendian.SerializeSigned(32, c.Min)
	if err != nil {
		return nil, err
	}
	maxBytes, err := bigbigendian.SerializeSigned(32, c.Max)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, onchainConfigEncodedLength)
	result = append(result, verBytes...)
	result = append(result, minBytes...)
	result = append(result, maxBytes...)
	return result, nil
}
