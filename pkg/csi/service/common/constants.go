/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package common

const (
	// MbInBytes is the number of bytes in one mebibyte.
	MbInBytes = int64(1024 * 1024)

	// GbInBytes is the number of bytes in one gibibyte.
	GbInBytes = int64(1024 * 1024 * 1024)

	// DefaultGbDiskSize is the default disk size in gibibytes.
	// TODO: will make the DefaultGbDiskSize configurable in the future
	DefaultGbDiskSize = int64(10)

	// DiskTypeBlockVolume is the value for the PersistentVolume's attribute "type"
	DiskTypeBlockVolume = "vSphere CNS Block Volume"

	// DiskTypeFileVolume is the value for the PersistentVolume's attribute "type"
	DiskTypeFileVolume = "vSphere CNS File Volume"

	// AttributeDiskType is a PersistentVolume's attribute.
	AttributeDiskType = "type"

	// AttributeDatastoreURL represents URL of the datastore in the StorageClass
	// For Example: DatastoreURL: "ds:///vmfs/volumes/5c9bb20e-009c1e46-4b85-0200483b2a97/"
	AttributeDatastoreURL = "datastoreurl"

	// AttributeStoragePolicyName represents name of the Storage Policy in the Storage Class
	// For Example: StoragePolicy: "vSAN Default Storage Policy"
	AttributeStoragePolicyName = "storagepolicyname"

	// AttributeStoragePolicyID represents Storage Policy Id in the Storage Classs
	// For Example: StoragePolicyId: "251bce41-cb24-41df-b46b-7c75aed3c4ee"
	AttributeStoragePolicyID = "storagepolicyid"

	// AttributeSupervisorStorageClass represents name of the Storage Class
	// For example: StorageClassName: "silver"
	AttributeSupervisorStorageClass = "svstorageclass"

	// AttributeFsType represents filesystem type in the Storage Classs
	// For Example: FsType: "ext4"
	AttributeFsType = "fstype"

	// AttributeAffineToHost represents the ESX host moid to which this PV should be affinitized
	// For Example: AffineToHost: "host-25"
	AttributeAffineToHost = "affinetohost"

	// AttributeStoragePool represents name of the StoragePool on which to place the PVC
	// For example: StoragePool: "storagepool-vsandatastore"
	AttributeStoragePool = "storagepool"

	// AttributeHostLocal represents the presence of HostLocal functionality in
	// the given storage policy. For Example: HostLocal: "True"
	AttributeHostLocal = "hostlocal"

	// HostMoidAnnotationKey represents the Node annotation key that has the value
	// of VC's ESX host moid of this node.
	HostMoidAnnotationKey = "vmware-system-esxi-node-moid"

	// Ext4FsType represents the default filesystem type for block volume
	Ext4FsType = "ext4"

	// NfsV4FsType represents nfs4 mount type
	NfsV4FsType = "nfs4"

	// NfsFsType represents nfs mount type
	NfsFsType = "nfs"

	//ProviderPrefix is the prefix used for the ProviderID set on the node
	// Example: vsphere://4201794a-f26b-8914-d95a-edeb7ecc4a8f
	ProviderPrefix = "vsphere://"

	// AttributeFirstClassDiskUUID is the SCSI Disk Identifier
	AttributeFirstClassDiskUUID = "diskUUID"

	// BlockVolumeType is the VolumeType for CNS Volume
	BlockVolumeType = "BLOCK"

	// FileVolumeType is the VolumeType for CNS File Share Volume
	FileVolumeType = "FILE"

	// Nfsv4AccessPointKey is the key for NFSv4 access point
	Nfsv4AccessPointKey = "NFSv4.1"

	// Nfsv4AccessPoint is the access point of file volume
	Nfsv4AccessPoint = "Nfsv4AccessPoint"

	// MinSupportedVCenterMajor is the minimum, major version of vCenter
	// on which CNS is supported.
	MinSupportedVCenterMajor int = 6

	// MinSupportedVCenterMinor is the minimum, minor version of vCenter
	// on which CNS is supported.
	MinSupportedVCenterMinor int = 7

	// MinSupportedVCenterPatch is the minimum patch version of vCenter
	// on which CNS is supported.
	MinSupportedVCenterPatch int = 3

	// VsanAffinityKey is the profile param key to indicate which node the FCD should be affinitized to.
	VsanAffinityKey string = "VSAN/affinity/affinity"

	// VsanAffinityMandatory is the profile param key to turn on affinity of the volume to a specific ESX host.
	VsanAffinityMandatory string = "VSAN/affinityMandatory/affinityMandatory"

	// VsanMigrateForDecom is the profile param key to set the migrate mode for the volume.
	VsanMigrateForDecom string = "VSAN/migrateForDecom/migrateForDecom"

	// VsanDatastoreType is the string to identify datastore type as vsan.
	VsanDatastoreType string = "vsan"

	// CSIMigrationParams helps identify if volume creation is requested by
	// in-tree storageclass or CSI storageclass
	CSIMigrationParams = "csimigration"

	// AttributeInitialVolumeFilepath represents the path of volume where volume is created
	AttributeInitialVolumeFilepath = "initialvolumefilepath"

	// DatastoreMigrationParam is used to supply datastore name for Volume provisioning
	DatastoreMigrationParam = "datastore-migrationparam"

	// DiskFormatMigrationParam supplies disk foramt (thin, thick, zeoredthick) for Volume provisioning
	DiskFormatMigrationParam = "diskformat-migrationparam"

	// HostFailuresToTolerateMigrationParam is raw vSAN Policy Parameter
	HostFailuresToTolerateMigrationParam = "hostfailurestotolerate-migrationparam"

	// ForceProvisioningMigrationParam is raw vSAN Policy Parameter
	ForceProvisioningMigrationParam = "forceprovisioning-migrationparam"

	// CacheReservationMigrationParam is raw vSAN Policy Parameter
	CacheReservationMigrationParam = "cachereservation-migrationparam"

	// DiskstripesMigrationParam is raw vSAN Policy Parameter
	DiskstripesMigrationParam = "diskstripes-migrationparam"

	// ObjectspacereservationMigrationParam is raw vSAN Policy Parameter
	ObjectspacereservationMigrationParam = "objectspacereservation-migrationparam"

	// IopslimitMigrationParam is raw vSAN Policy Parameter
	IopslimitMigrationParam = "iopslimit-migrationparam"

	// AnnMigratedTo annotation is added to a PVC and PV that is supposed to be
	// provisioned/deleted by its corresponding CSI driver
	AnnMigratedTo = "pv.kubernetes.io/migrated-to"

	// IPs is Client IP address, IP range or IP subnet
	IPs string = "ips"

	// CSINamespace is the namespace of pvCSI in TKC Cluster
	CSINamespace = "vmware-system-csi"

	// CSIFeatureStatesConfigMapName is the name of configmap to store FSS values
	CSIFeatureStatesConfigMapName = "csi-feature-states"
)

// Supported container orchestrators
const (
	Kubernetes = iota // Default container orchestor for TKC, Supervisor Cluster and Vanilla K8s
)

// Feature state flag names
const (
	// VolumeHealth is the feature flag name for volume health
	VolumeHealth = "volume-health"
	// VolumeExtend is feature flag name for volume expansion
	VolumeExtend = "volume-extend"
)
