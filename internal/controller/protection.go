package controller

import "strings"

// DefaultFolderTargetPatterns are the default patterns used to identify CI/test folders.
var DefaultFolderTargetPatterns = []string{"ci-", "user-", "build-"}

// DefaultResourcePoolTargetPrefixes are the default prefixes used to identify CI/test resource pools
// that are candidates for cleanup. Resource pools whose names start with these prefixes may be
// deleted if they are empty and not explicitly protected via the protection config.
var DefaultResourcePoolTargetPrefixes = []string{"ci-", "qeci-"}

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

// IsProtectedResourcePool returns true if rpName starts with any of the protected prefixes.
// Protected resource pools are exempt from cleanup even if they match target prefixes.
func IsProtectedResourcePool(rpName string, protectedPrefixes []string) bool {
	for _, prefix := range protectedPrefixes {
		if strings.HasPrefix(rpName, prefix) {
			return true
		}
	}
	return false
}

// IsProtectedVirtualMachine returns true if vmName starts with any of the protected prefixes.
// Protected VMs are exempt from cleanup during port group deletion.
func IsProtectedVirtualMachine(vmName string, protectedPrefixes []string) bool {
	for _, prefix := range protectedPrefixes {
		if strings.HasPrefix(vmName, prefix) {
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
