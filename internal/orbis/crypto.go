// Package orbis implements the PS4 PKG and PFS formats natively in Go so that
// PS2 to PS4 conversion works without Windows publishing tools or Wine.
//
// The structures follow the documentation and reference implementation of
// LibOrbisPkg (https://github.com/OpenOrbis/LibOrbisPkg), which is the basis of
// every open source fake PKG tool.
package orbis

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
)

func sha256sum(data ...[]byte) []byte {
	digest := sha256.New()
	for _, chunk := range data {
		digest.Write(chunk)
	}
	return digest.Sum(nil)
}

func hmacSHA256(key []byte, data ...[]byte) []byte {
	mac := hmac.New(sha256.New, key)
	for _, chunk := range data {
		mac.Write(chunk)
	}
	return mac.Sum(nil)
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// computeKeys derives the passcode key with the given index for a package.
// Index 1 is the EKPFS, which unlocks the PFS image.
func computeKeys(contentID, passcode string, index uint32) ([]byte, error) {
	if len(contentID) != 36 {
		return nil, fmt.Errorf("content ID must be 36 characters, got %d", len(contentID))
	}
	if len(passcode) != 32 {
		return nil, fmt.Errorf("passcode must be 32 characters, got %d", len(passcode))
	}
	indexBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(indexBytes, index)
	padded := make([]byte, 48)
	copy(padded, contentID)
	data := make([]byte, 0, 96)
	data = append(data, sha256sum(indexBytes)...)
	data = append(data, sha256sum(padded)...)
	data = append(data, passcode...)
	return sha256sum(data), nil
}

// pfsGenCryptoKey is the common PFS key derivation from the FPKG code.
func pfsGenCryptoKey(ekpfs, seed []byte, index uint32) []byte {
	input := make([]byte, 4+len(seed))
	binary.LittleEndian.PutUint32(input, index)
	copy(input[4:], seed)
	return hmacSHA256(ekpfs, input)
}

// pfsGenEncKey returns the (tweak, data) key pair used for AES-XTS.
func pfsGenEncKey(ekpfs, seed []byte, newCrypt bool) (tweak, data []byte) {
	key := ekpfs
	if newCrypt {
		key = hmacSHA256(ekpfs, seed)
	}
	encKey := pfsGenCryptoKey(key, seed, 1)
	return encKey[:16], encKey[16:32]
}

// pfsGenSignKey returns the HMAC key used to sign PFS blocks.
func pfsGenSignKey(ekpfs, seed []byte) []byte {
	return pfsGenCryptoKey(ekpfs, seed, 2)
}

// createKeystone builds the sce_sys/keystone file for the given passcode.
func createKeystone(passcode string) []byte {
	header := []byte{
		0x6b, 0x65, 0x79, 0x73, 0x74, 0x6f, 0x6e, 0x65, 0x02, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	fingerprint := hmacSHA256(keystoneHmacKey, []byte(passcode))
	body := append(append([]byte{}, header...), fingerprint...)
	return append(body, hmacSHA256(keystoneMacData, body)...)
}

// aesCBCNoPadding encrypts or decrypts in place with AES-CBC and no padding.
func aesCBCNoPadding(buffer, key, iv []byte, encrypt bool) error {
	if len(buffer)%aes.BlockSize != 0 {
		return fmt.Errorf("AES-CBC input must be a multiple of %d bytes, got %d", aes.BlockSize, len(buffer))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	if encrypt {
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(buffer, buffer)
	} else {
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(buffer, buffer)
	}
	return nil
}

// rsa2048Encrypt performs a raw modular exponentiation on a big-endian value.
func rsa2048Encrypt(value, modulus []byte, exponent int64) []byte {
	message := new(big.Int).SetBytes(value)
	result := new(big.Int).Exp(message, big.NewInt(exponent), new(big.Int).SetBytes(modulus))
	out := make([]byte, 256)
	result.FillBytes(out)
	return out
}

// rsa2048EncryptKey encrypts a 32-byte key or digest with the given public
// modulus, using the deterministic Mersenne Twister padding Sony's tools use.
func rsa2048EncryptKey(modulus, hash []byte) []byte {
	buffer := make([]byte, 0, 288)
	buffer = append(buffer, modulus[:256]...)
	buffer = append(buffer, hash[:32]...)
	finalHash := sha256sum(sha256sum(buffer))
	seed := make([]uint32, 8)
	for i := 0; i < 8; i++ {
		seed[i] = binary.BigEndian.Uint32(finalHash[i*4:])
	}
	mt := newMersenneTwisterFromSlice(seed)

	padded := make([]byte, 256)
	padded[0] = 0
	padded[1] = 2
	padded[223] = 0
	copy(padded[224:], hash[:32])
	shaSource := make([]byte, 48)
	for k := 2; k < 223; {
		for i := 0; i < 12; i++ {
			binary.BigEndian.PutUint32(shaSource[i*4:], mt.next())
		}
		for _, r := range sha256sum(shaSource) {
			if k >= 223 {
				break
			}
			if r != 0 {
				padded[k] = r
				k++
			}
		}
	}
	return rsa2048Encrypt(padded, modulus, 65537)
}

// privateKey rebuilds a Go RSA private key from a big-endian keyset.
func (k rsaKeyset) privateKey() (*rsa.PrivateKey, error) {
	key := &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{
			N: new(big.Int).SetBytes(k.Modulus),
			E: int(new(big.Int).SetBytes(k.PublicExponent).Int64()),
		},
		D: new(big.Int).SetBytes(k.PrivateExponent),
		Primes: []*big.Int{
			new(big.Int).SetBytes(k.Prime1),
			new(big.Int).SetBytes(k.Prime2),
		},
	}
	key.Precompute()
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("invalid embedded RSA keyset: %w", err)
	}
	return key, nil
}

// rsa2048SignSHA256 signs a SHA-256 digest with PKCS#1 v1.5 padding.
func rsa2048SignSHA256(digest []byte, keyset rsaKeyset) ([]byte, error) {
	key, err := keyset.privateKey()
	if err != nil {
		return nil, err
	}
	return rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest)
}

// xtsTransform is an AES-XTS-128 sector transformer.
type xtsTransform struct {
	data  cipher.Block
	tweak cipher.Block
}

func newXTSTransform(dataKey, tweakKey []byte) (*xtsTransform, error) {
	data, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}
	tweak, err := aes.NewCipher(tweakKey)
	if err != nil {
		return nil, err
	}
	return &xtsTransform{data: data, tweak: tweak}, nil
}

func (x *xtsTransform) encryptSector(sector []byte, number uint64) { x.crypt(sector, number, true) }
func (x *xtsTransform) decryptSector(sector []byte, number uint64) { x.crypt(sector, number, false) }

func (x *xtsTransform) crypt(sector []byte, number uint64, encrypt bool) {
	var tweak [16]byte
	binary.LittleEndian.PutUint64(tweak[:8], number)
	var encryptedTweak [16]byte
	x.tweak.Encrypt(encryptedTweak[:], tweak[:])
	var scratch [16]byte
	for offset := 0; offset+16 <= len(sector); offset += 16 {
		block := sector[offset : offset+16]
		for i := 0; i < 16; i++ {
			scratch[i] = block[i] ^ encryptedTweak[i]
		}
		if encrypt {
			x.data.Encrypt(scratch[:], scratch[:])
		} else {
			x.data.Decrypt(scratch[:], scratch[:])
		}
		for i := 0; i < 16; i++ {
			block[i] = scratch[i] ^ encryptedTweak[i]
		}
		feedback := byte(0)
		for i := 0; i < 16; i++ {
			current := encryptedTweak[i]
			encryptedTweak[i] = current<<1 | feedback
			feedback = (current & 0x80) >> 7
		}
		if feedback != 0 {
			encryptedTweak[0] ^= 0x87
		}
	}
}
