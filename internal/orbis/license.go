package orbis

import (
	"encoding/binary"
	"math"
)

// licenseFiles holds the generated license.dat and license.info entries.
type licenseFiles struct {
	dat  []byte
	info []byte
}

const (
	licenseTypeDebug = 0x200
)

// newLicenseDat builds a signed debug license for a fake package.
func newLicenseDat(contentID string, contentType uint32) (*licenseFiles, error) {
	contentIDPadded := make([]byte, 48)
	copy(contentIDPadded, contentID)
	digest := sha256sum(contentIDPadded)
	secretIV := append([]byte(nil), digest[:16]...)
	secret := make([]byte, 144)
	copy(secret, digest[16:32])
	if err := aesCBCNoPadding(secret, rifDebugKey, secretIV, true); err != nil {
		return nil, err
	}

	skuFlag := int16(0)
	if contentType == contentTypeGD {
		// ShellCore requires this for game data packages.
		skuFlag = 3
	}

	dat := make([]byte, 0x400)
	binary.BigEndian.PutUint32(dat[0x00:], 0x52494600) // "RIF\0"
	binary.BigEndian.PutUint16(dat[0x04:], 1)          // version
	binary.BigEndian.PutUint16(dat[0x06:], 0xFFFF)     // unknown, -1
	binary.BigEndian.PutUint64(dat[0x08:], 0)          // PSN account ID
	binary.BigEndian.PutUint64(dat[0x10:], 1364222275)
	binary.BigEndian.PutUint64(dat[0x18:], uint64(math.MaxInt64))
	copy(dat[0x20:], contentID)
	binary.BigEndian.PutUint16(dat[0x50:], licenseTypeDebug)
	binary.BigEndian.PutUint16(dat[0x52:], drmTypePS4)
	binary.BigEndian.PutUint16(dat[0x54:], uint16(contentType))
	binary.BigEndian.PutUint16(dat[0x56:], uint16(skuFlag))
	binary.BigEndian.PutUint32(dat[0x58:], 0) // flags
	binary.BigEndian.PutUint32(dat[0x5C:], 0)
	binary.BigEndian.PutUint32(dat[0x60:], 0)
	binary.BigEndian.PutUint32(dat[0x64:], 1)
	binary.BigEndian.PutUint32(dat[0x68:], 0)
	// 0x6C..0x240: reserved, 0x240: disc key (zero)
	copy(dat[0x260:], secretIV)
	copy(dat[0x270:], secret)
	signature, err := rsa2048SignSHA256(sha256sum(dat[:0x300]), debugRifKeyset)
	if err != nil {
		return nil, err
	}
	copy(dat[0x300:], signature)

	info := make([]byte, 0x200)
	copy(info[0x00:], contentID)
	// 0x30: entitlement key (zero for a fake package)
	binary.BigEndian.PutUint32(info[0x40:], 0)
	binary.BigEndian.PutUint32(info[0x44:], contentType)
	binary.BigEndian.PutUint32(info[0x48:], 0)
	binary.BigEndian.PutUint32(info[0x4C:], 1)
	return &licenseFiles{dat: dat, info: info}, nil
}
