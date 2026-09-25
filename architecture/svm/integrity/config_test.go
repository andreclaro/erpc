package integrity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckSet_NilSetForIsDisabled(t *testing.T) {
	var s CheckSet
	assert.False(t, s.For("svm.struct.txShape").Enabled)
}

func TestCheckSet_EnableIsFluent(t *testing.T) {
	s := make(CheckSet).
		Enable("a", map[string]string{"k": "v"}).
		Enable("b", nil)
	assert.True(t, s.For("a").Enabled)
	assert.Equal(t, "v", s.For("a").Params["k"])
	assert.True(t, s.For("b").Enabled)
	assert.Nil(t, s.For("b").Params)
	assert.False(t, s.For("missing").Enabled)
}

func TestCheckConfig_ParamDefaults(t *testing.T) {
	c := CheckConfig{}
	assert.Equal(t, "dflt", c.param("x", "dflt"), "missing key -> default")
	c = CheckConfig{Params: map[string]string{"x": "1"}}
	assert.Equal(t, "1", c.param("x", "dflt"))
	assert.Equal(t, "dflt", c.param("y", "dflt"))
}

func TestCheckConfig_IntParamFallsBackOnGarbage(t *testing.T) {
	c := CheckConfig{Params: map[string]string{"good": "42", "bad": "NaN"}}
	assert.Equal(t, 42, c.intParam("good", 7))
	assert.Equal(t, 7, c.intParam("bad", 7), "unparseable -> default")
	assert.Equal(t, 7, c.intParam("absent", 7))
	var nilParams CheckConfig
	assert.Equal(t, 7, nilParams.intParam("x", 7), "nil params -> default")
}
