package agent

import "testing"

func TestContextUsePercent(t *testing.T) {
	for _, c := range []struct {
		use  ContextUse
		want int
	}{
		{ContextUse{126000, 200000}, 63},
		{ContextUse{6051, 200000}, 3},
		{ContextUse{300000, 200000}, 100},
		{ContextUse{5, 0}, 0},
	} {
		if got := c.use.Percent(); got != c.want {
			t.Errorf("%+v = %d%%, want %d%%", c.use, got, c.want)
		}
	}
}
