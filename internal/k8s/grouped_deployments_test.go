// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// These tests check that grouped Deployment names expose member roles while
// preserving separate names for complete owner, member, and build identities.
package k8s

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestGroupedDeploymentNamesKeepMemberAndBuildReadable(t *testing.T) {
	for _, test := range []struct {
		name   string
		member string
		build  string
		prefix string
	}{
		{"primary", "primary", "release-1", "primary-release-1-"},
		{"activity workers", "activities", "release-123456789", "activities-release-12-"},
		{"cleaned build", "query-provider", "Release:1.2", "query-provider-release-1--"},
		{"below member allowance", strings.Repeat("m", 24), strings.Repeat("b", 63), strings.Repeat("m", 24) + "-bbbbbbbbbb-"},
		{"at member allowance", strings.Repeat("m", 25), strings.Repeat("b", 63), strings.Repeat("m", 25) + "-bbbbbbbbbb-"},
		{"above member allowance", strings.Repeat("m", 26), strings.Repeat("b", 63), strings.Repeat("m", 25) + "-bbbbbbbbbb-"},
		{"maximum member", strings.Repeat("m", 63), strings.Repeat("b", 63), strings.Repeat("m", 25) + "-bbbbbbbbbb-"},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := computeGroupedDeploymentName(strings.Repeat("owner", 12), test.member, test.build)
			assert.True(t, strings.HasPrefix(name, test.prefix), name)
			assert.Len(t, name, len(test.prefix)+10)
			assert.LessOrEqual(t, len(name), MaxDeploymentNameLen)
			assert.Empty(t, validation.IsDNS1123Label(name))
			assert.Equal(t, name, computeGroupedDeploymentName(strings.Repeat("owner", 12), test.member, test.build))
		})
	}
}

func TestGroupedDeploymentNameHashIncludesCompleteSeparateIdentities(t *testing.T) {
	for _, test := range []struct {
		name         string
		firstOwner   string
		firstMember  string
		firstBuild   string
		secondOwner  string
		secondMember string
		secondBuild  string
	}{
		{"owner after prefix", "long-owner-one", "activities", "release-1", "long-owner-two", "activities", "release-1"},
		{"member after prefix", "group", strings.Repeat("m", 25) + "-one", "release-1", "group", strings.Repeat("m", 25) + "-two", "release-1"},
		{"build after prefix", "group", "activities", "release-123-one", "group", "activities", "release-123-two"},
		{"cleaned build collision", "group", "activities", "Release:1", "group", "activities", "release-1"},
		{"separate tuple fields", "group-a", "b", "release-1", "group", "a-b", "release-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := computeGroupedDeploymentName(test.firstOwner, test.firstMember, test.firstBuild)
			second := computeGroupedDeploymentName(test.secondOwner, test.secondMember, test.secondBuild)
			assert.NotEqual(t, first, second)
			assert.NotEqual(t, first[len(first)-10:], second[len(second)-10:])
		})
	}
}
