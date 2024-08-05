package internal

const (
	StatusOK = iota
	StatusInvalidArtifactOrVolumeID
	StatusVolumeBusy

	StatusCommitErr
	StatusTargetRefMiss
	StatusMetaError
	StatusPushError
	StatusInvalidManifest
	StatusFetchManifestFailed
	StatusCreateVolumeError
	StatusDetachVolumeError
	StatusFetchManifestError

	StatusAuthenticationFailed
	StatusUnauthorized
	StatusListVolumeFailed
	StatusRemoveVolumeDirErr
	StatusCleanVolumeMetaErr
	StatusArtifactNotFound
	StatusInvalidSecret
	StatusInvalidArtifactType
	StatusSaveManifestFailed
	StatusLoadManifestFailed
	StatusUpdateVolumeConfigFailed

	StatusLoadVolumeConfigFailed
	StatusRemoveObjectFailed
)
