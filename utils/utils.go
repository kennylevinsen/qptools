package utils

import (
	"encoding/binary"
	"errors"

	"github.com/joushou/qp"
)

// Readdir interprets a 9p2000 directory listing.
func Readdir(b []byte) ([]qp.Stat, error) {
	var stats []qp.Stat
	for len(b) > 0 {
		l := int(binary.LittleEndian.Uint16(b[0:2]))
		if l+2 > len(b) {
			return stats, errors.New("short input")
		}

		var stat qp.Stat
		stat.UnmarshalBinary(b[0 : 2+l])
		b = b[2+l:]
		stats = append(stats, stat)
	}

	return stats, nil
}
