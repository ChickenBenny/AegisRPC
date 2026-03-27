package capability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCapability_Has(t *testing.T) {
	c := CapBasic | CapHistorical

	assert.True(t, c.Has(CapBasic))
	assert.True(t, c.Has(CapHistorical))
	assert.False(t, c.Has(CapDebug))
	assert.False(t, c.Has(CapTrace))
}

func TestCapability_Has_Multiple(t *testing.T) {
	c := CapBasic | CapHistorical | CapDebug

	assert.True(t, c.Has(CapBasic|CapHistorical)) // 兩個都有
	assert.False(t, c.Has(CapBasic|CapTrace))     // Trace 沒有
}

func TestCapability_String(t *testing.T) {
	assert.Equal(t, "basic", CapBasic.String())
	assert.Equal(t, "basic,historical", (CapBasic|CapHistorical).String())
	assert.Equal(t, "basic,historical,debug,trace", (CapBasic|CapHistorical|CapDebug|CapTrace).String())
	assert.Equal(t, "none", Capability(0).String())
}
