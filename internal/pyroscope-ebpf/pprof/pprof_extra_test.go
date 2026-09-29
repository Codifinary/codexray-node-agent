package pprof

import (
	"bytes"
	"testing"

	"github.com/google/pprof/profile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func memSample(stack []string, objects, space uint64) *ProfileSample {
	return &ProfileSample{
		Pid:         1,
		Target:      testTarget,
		SampleType:  SampleTypeMem,
		Aggregation: SampleAggregated,
		Stack:       stack,
		Value:       objects,
		Value2:      space,
	}
}

func TestMemProfileBuilder(t *testing.T) {
	builders := NewProfileBuilders(BuildersOptions{
		SampleRate: 97,
	})

	builders.AddSample(memSample([]string{"a", "b", "c"}, 10, 1024))

	builder := builders.BuilderForSample(memSample([]string{"a", "b", "c"}, 0, 0))
	require.Len(t, builder.Profile.SampleType, 2)
	assert.Equal(t, "alloc_objects", builder.Profile.SampleType[0].Type)
	assert.Equal(t, "alloc_space", builder.Profile.SampleType[1].Type)
	assert.Equal(t, "space", builder.Profile.PeriodType.Type)

	buf := bytes.NewBuffer(nil)
	_, err := builder.Write(buf)
	assert.NoError(t, err)

	parsed, err := profile.Parse(bytes.NewBuffer(buf.Bytes()))
	assert.NoError(t, err)
	require.Len(t, parsed.Sample, 1)
	assert.Equal(t, int64(10), parsed.Sample[0].Value[0])
	assert.Equal(t, int64(1024), parsed.Sample[0].Value[1])
}

func TestCollect(t *testing.T) {
	builders := NewProfileBuilders(BuildersOptions{SampleRate: 97})

	collector := fakeCollector{samples: []ProfileSample{
		*sample([]string{"a", "b"}, 1),
		*sample([]string{"a", "c"}, 2),
	}}

	err := Collect(builders, collector)
	require.NoError(t, err)

	builder := builders.BuilderForSample(sample([]string{"a", "b"}, 0))
	require.Len(t, builder.Profile.Sample, 2)
}

type fakeCollector struct {
	samples []ProfileSample
}

func (f fakeCollector) CollectProfiles(callback CollectProfilesCallback) error {
	for _, s := range f.samples {
		callback(s)
	}
	return nil
}

func TestPerPIDProfile(t *testing.T) {
	builders := NewProfileBuilders(BuildersOptions{
		SampleRate:    97,
		PerPIDProfile: true,
	})

	s1 := sample([]string{"a", "b"}, 1)
	s1.Pid = 111
	s2 := sample([]string{"a", "b"}, 1)
	s2.Pid = 222

	builders.AddSample(s1)
	builders.AddSample(s2)

	// Different PIDs with the same labels should get distinct builders.
	require.Len(t, builders.Builders, 2)
}

func TestUint64BytesEmpty(t *testing.T) {
	require.Nil(t, uint64Bytes(nil))
	require.Nil(t, uint64Bytes([]uint64{}))
	require.NotNil(t, uint64Bytes([]uint64{1, 2, 3}))
}
