// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// These tests check configured Deployment prefixes, distinct complete identities,
// and the original naming behavior for callers without a configured prefix.
package k8s

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestGroupedDeploymentNamesUseConfiguredPrefix(t *testing.T) {
	for _, test := range []struct {
		name, prefix, build, want string
	}{
		{"primary", "workflow-worker", "release-1", "workflow-worker-release-1-"},
		{"activities", "activity-worker", "release-123456789", "activity-worker-release-12-"},
		{"cleaned build", "query-provider", "Release:1.2", "query-provider-release-1--"},
		{"below allowance", strings.Repeat("m", 24), strings.Repeat("b", 63), strings.Repeat("m", 24) + "-bbbbbbbbbb-"},
		{"at allowance", strings.Repeat("m", 25), strings.Repeat("b", 63), strings.Repeat("m", 25) + "-bbbbbbbbbb-"},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := computeGroupedDeploymentName("worker-group", "primary", test.prefix, test.build)
			assert.True(t, strings.HasPrefix(name, test.want), name)
			assert.Len(t, name, len(test.want)+10)
			assert.LessOrEqual(t, len(name), MaxDeploymentNameLen)
			assert.Empty(t, validation.IsDNS1123Label(name))
			assert.Equal(t, name, computeGroupedDeploymentName("worker-group", "primary", test.prefix, test.build))
		})
	}
}

func TestGroupedDeploymentNamesWithoutPrefixKeepOriginalBehavior(t *testing.T) {
	for _, owner := range []string{"group", strings.Repeat("group", 12)} {
		for _, member := range []string{PrimaryMember, "activities"} {
			for _, build := range []string{"release-1", strings.Repeat("release", 9)} {
				assert.Equal(t, ComputeVersionedDeploymentName(owner+"-"+member, build), computeGroupedDeploymentName(owner, member, "", build))
			}
		}
	}
}

func TestGroupedDeploymentNameHashIncludesCompleteSeparateIdentities(t *testing.T) {
	for _, test := range []struct {
		name, firstOwner, firstMember, firstBuild, secondOwner, secondMember, secondBuild string
	}{
		{"owner", "long-owner-one", "activities", "release-1", "long-owner-two", "activities", "release-1"},
		{"same prefix across members", "group", "first", "release-1", "group", "second", "release-1"},
		{"build after prefix", "group", "activities", "release-123-one", "group", "activities", "release-123-two"},
		{"cleaned build collision", "group", "activities", "Release:1", "group", "activities", "release-1"},
		{"separate tuple fields", "group-a", "b", "release-1", "group", "a-b", "release-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := computeGroupedDeploymentName(test.firstOwner, test.firstMember, "worker", test.firstBuild)
			second := computeGroupedDeploymentName(test.secondOwner, test.secondMember, "worker", test.secondBuild)
			assert.NotEqual(t, first, second)
			assert.NotEqual(t, first[len(first)-10:], second[len(second)-10:])
		})
	}
}

func TestSavedDeploymentNamePrefixValidation(t *testing.T) {
	for _, test := range []struct {
		name, prefix string
		valid        bool
	}{
		{"omitted", "", true},
		{"one", "a", true},
		{"below allowance", strings.Repeat("a", 24), true},
		{"at allowance", strings.Repeat("a", 25), true},
		{"above allowance", strings.Repeat("a", 26), false},
		{"uppercase", "Worker", false},
		{"leading dash", "-worker", false},
		{"space", "activity worker", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateSavedDeploymentNamePrefix(test.prefix)
			if test.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
