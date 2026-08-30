package orbis

// mersenneTwister is the MT19937 variant used by the PS4 publishing tools to
// pad RSA inputs deterministically.
type mersenneTwister struct {
	state [624]uint32
	index uint32
}

const (
	mtDefaultSeed = 0x12BD6AA
	mtMatrixA     = 0x9908b0df
	mtUpperMask   = 0x80000000
	mtLowerMask   = 0x7fffffff
	mtConstant1   = 0x6C078965
	mtConstant2   = 0x19660D
	mtConstant3   = 0x5D588B65
	mtConstant4   = 0x9d2c5680
	mtConstant5   = 0xefc60000
)

func newMersenneTwister(seed uint32) *mersenneTwister {
	mt := &mersenneTwister{}
	mt.state[0] = seed
	for i := uint32(1); i < uint32(len(mt.state)); i++ {
		mt.state[i] = i + mtConstant1*(mt.state[i-1]^(mt.state[i-1]>>30))
	}
	mt.index = uint32(len(mt.state))
	return mt
}

func newMersenneTwisterFromSlice(seed []uint32) *mersenneTwister {
	mt := newMersenneTwister(mtDefaultSeed)
	n := uint32(len(mt.state))
	stateIdx, seedIdx := uint32(1), uint32(0)
	length := len(mt.state)
	if len(seed) > length {
		length = len(seed)
	}
	for ; length > 0; length-- {
		mt.state[stateIdx] = (mt.state[stateIdx] ^ ((mt.state[stateIdx-1] ^ (mt.state[stateIdx-1] >> 30)) * mtConstant2)) + seed[seedIdx] + seedIdx
		stateIdx++
		seedIdx++
		if stateIdx >= n {
			mt.state[0] = mt.state[n-1]
			stateIdx = 1
		}
		if seedIdx >= uint32(len(seed)) {
			seedIdx = 0
		}
	}
	for i := 0; i < len(mt.state)-1; i++ {
		mt.state[stateIdx] = (mt.state[stateIdx] ^ ((mt.state[stateIdx-1] ^ (mt.state[stateIdx-1] >> 30)) * mtConstant3)) - stateIdx
		stateIdx++
		if stateIdx >= n {
			mt.state[0] = mt.state[n-1]
			stateIdx = 1
		}
	}
	mt.state[0] = 1 << 31
	// The reference implementation leaves the generator ready to refill on the
	// first call, matching the C# constructor which never resets mti.
	mt.index = uint32(len(mt.state))
	return mt
}

func (m *mersenneTwister) next() uint32 {
	const n = 624
	const mConst = 397
	mag01 := [2]uint32{0, mtMatrixA}
	var y uint32
	if m.index >= n {
		var kk uint32
		for kk = 0; kk < n-mConst; kk++ {
			y = (m.state[kk] & mtUpperMask) | (m.state[kk+1] & mtLowerMask)
			m.state[kk] = m.state[kk+mConst] ^ ((y >> 1) & 0x7fffffff) ^ mag01[y&1]
		}
		for ; kk < n-1; kk++ {
			y = (m.state[kk] & mtUpperMask) | (m.state[kk+1] & mtLowerMask)
			m.state[kk] = m.state[kk+mConst-n] ^ ((y >> 1) & 0x7fffffff) ^ mag01[y&1]
		}
		y = (m.state[n-1] & mtUpperMask) | (m.state[0] & mtLowerMask)
		m.state[n-1] = m.state[mConst-1] ^ ((y >> 1) & 0x7fffffff) ^ mag01[y&1]
		m.index = 0
	}
	y = m.state[m.index]
	m.index++
	y ^= (y >> 11) & 0x1fffff
	y ^= (y << 7) & mtConstant4
	y ^= (y << 15) & mtConstant5
	y ^= (y >> 18) & 0x3fff
	return y
}
