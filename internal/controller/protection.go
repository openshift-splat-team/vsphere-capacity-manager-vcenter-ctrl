package controller

import "strings"

// DefaultFolderTargetPatterns are the default patterns used to identify CI/test folders.
var DefaultFolderTargetPatterns = []string{"ci-", "user-", "build-"}

// IsProtectedTag returns true if tagName starts with any of the protected prefixes.
func IsProtectedTag(tagName string, protectedPrefixes []string) bool {
	for _, prefix := range protectedPrefixes {
		if strings.HasPrefix(tagName, prefix) {
			return true
		}
	}
	return false
}

// IsTargetFolder returns true if folderName contains any of the target patterns.
func IsTargetFolder(folderName string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(folderName, pattern) {
			return true
		}
	}
	return false
}

// IsProtectedFolder returns true if folderName exactly matches any protected folder name.
func IsProtectedFolder(folderName string, protectedFolders []string) bool {
	for _, pf := range protectedFolders {
		if folderName == pf {
			return true
		}
	}
	return false
}

// IsTargetResourcePool returns true if rpName starts with any of the target prefixes.
func IsTargetResourcePool(rpName string, targetPrefixes []string) bool {
	for _, prefix := range targetPrefixes {
		if strings.HasPrefix(rpName, prefix) {
			return true
		}
	}
	return false
}

// IsTargetStoragePolicy returns true if policyName starts with the given prefix.
func IsTargetStoragePolicy(policyName string, prefix string) bool {
	return strings.HasPrefix(policyName, prefix)
}

// IsTargetCNSVolume returns true if clusterId starts with any of the target prefixes.
func IsTargetCNSVolume(clusterId string, targetPrefixes []string) bool {
	for _, prefix := range targetPrefixes {
		if strings.HasPrefix(clusterId, prefix) {
			return true
		}
	}
	return false
}

// IsKubevolExpired returns true if ageInDays meets or exceeds minAgeDays.
func IsKubevolExpired(ageInDays, minAgeDays int) bool {
	return ageInDays >= minAgeDays
}
