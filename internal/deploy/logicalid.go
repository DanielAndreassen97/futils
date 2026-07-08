package deploy

import "strings"

// placeholderGUID is fabric's reserved "unset logicalId" value; never replaced.
const placeholderGUID = "00000000-0000-0000-0000-000000000000"

// ReplaceLogicalIds rewrites every known source logicalId in content with its
// deployed target GUID. This is how cross-item references (e.g. a report
// pointing at a semantic model's logicalId) are repointed at the items that
// were just created in the target workspace. Requires that referenced items
// are published first (see order.go).
func ReplaceLogicalIds(content []byte, idMap map[string]string) []byte {
	s := string(content)
	for logicalID, guid := range idMap {
		if logicalID == placeholderGUID || logicalID == "" {
			continue
		}
		s = strings.ReplaceAll(s, logicalID, guid)
	}
	return []byte(s)
}
