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

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/test/e2e/framework"
	fnodes "k8s.io/kubernetes/test/e2e/framework/node"
	fpod "k8s.io/kubernetes/test/e2e/framework/pod"
	fpv "k8s.io/kubernetes/test/e2e/framework/pv"
	"sigs.k8s.io/vsphere-csi-driver/pkg/csi/service/logger"
	k8s "sigs.k8s.io/vsphere-csi-driver/pkg/kubernetes"
)

var _ = ginkgo.Describe("Basic Static Provisioning", func() {
	f := framework.NewDefaultFramework("e2e-csistaticprovision")

	var (
		client                     clientset.Interface
		namespace                  string
		fcdID                      string
		pv                         *v1.PersistentVolume
		pvc                        *v1.PersistentVolumeClaim
		defaultDatacenter          *object.Datacenter
		defaultDatastore           *object.Datastore
		deleteFCDRequired          bool
		pandoraSyncWaitTime        int
		err                        error
		datastoreURL               string
		storagePolicyName          string
		isVsanhealthServiceStopped bool
		isSPSserviceStopped        bool
		ctx                        context.Context
	)

	ginkgo.BeforeEach(func() {
		bootstrap()
		client = f.ClientSet
		namespace = getNamespaceToRunTests(f)
		nodeList, err := fnodes.GetReadySchedulableNodes(f.ClientSet)
		framework.ExpectNoError(err, "Unable to find ready and schedulable Node")
		storagePolicyName = GetAndExpectStringEnvVar(envStoragePolicyNameForSharedDatastores)
		if !(len(nodeList.Items) > 0) {
			framework.Failf("Unable to find ready and schedulable Node")
		}

		if os.Getenv(envPandoraSyncWaitTime) != "" {
			pandoraSyncWaitTime, err = strconv.Atoi(os.Getenv(envPandoraSyncWaitTime))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		} else {
			pandoraSyncWaitTime = defaultPandoraSyncWaitTime
		}
		deleteFCDRequired = false
		isVsanhealthServiceStopped = false
		isSPSserviceStopped = false
		var datacenters []string
		datastoreURL = GetAndExpectStringEnvVar(envSharedDatastoreURL)
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
		finder := find.NewFinder(e2eVSphere.Client.Client, false)
		cfg, err := getConfig()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		dcList := strings.Split(cfg.Global.Datacenters, ",")
		for _, dc := range dcList {
			dcName := strings.TrimSpace(dc)
			if dcName != "" {
				datacenters = append(datacenters, dcName)
			}
		}

		for _, dc := range datacenters {
			defaultDatacenter, err = finder.Datacenter(ctx, dc)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			finder.SetDatacenter(defaultDatacenter)
			defaultDatastore, err = getDatastoreByURL(ctx, datastoreURL, defaultDatacenter)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	})

	ginkgo.AfterEach(func() {
		vcAddress := e2eVSphere.Config.Global.VCenterHostname + ":" + sshdPort
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ginkgo.By("Performing test cleanup")
		if deleteFCDRequired {
			ginkgo.By(fmt.Sprintf("Deleting FCD: %s", fcdID))

			err := e2eVSphere.deleteFCD(ctx, fcdID, defaultDatastore.Reference())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if pvc != nil {
			framework.ExpectNoError(fpv.DeletePersistentVolumeClaim(client, pvc.Name, namespace), "Failed to delete PVC ", pvc.Name)
		}

		if pv != nil {
			framework.ExpectNoError(framework.WaitForPersistentVolumeDeleted(client, pv.Name, poll, pollTimeoutShort))
			framework.ExpectNoError(e2eVSphere.waitForCNSVolumeToBeDeleted(pv.Spec.CSI.VolumeHandle))
		}

		if isVsanhealthServiceStopped {
			ginkgo.By(fmt.Sprintln("Starting vsan-health on the vCenter host"))
			err = invokeVCenterServiceControl("start", vsanhealthServiceName, vcAddress)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow vsan-health to come up again", vsanHealthServiceWaitTime))
			time.Sleep(time.Duration(vsanHealthServiceWaitTime) * time.Second)
		}

		if isSPSserviceStopped {
			ginkgo.By(fmt.Sprintln("Starting sps on the vCenter host"))
			err = invokeVCenterServiceControl("start", "sps", vcAddress)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow sps to come up again", vsanHealthServiceWaitTime))
			time.Sleep(time.Duration(vsanHealthServiceWaitTime) * time.Second)
		}

	})

	staticProvisioningPreSetUpUtil := func(ctx context.Context) (*restclient.Config, *storagev1.StorageClass, string) {
		log := logger.GetLogger(ctx)

		namespace = getNamespaceToRunTests(f)
		// Get a config to talk to the apiserver
		k8senv := GetAndExpectStringEnvVar("KUBECONFIG")
		restConfig, err := clientcmd.BuildConfigFromFlags("", k8senv)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Get storage Policy")
		ginkgo.By(fmt.Sprintf("storagePolicyName: %s", storagePolicyName))
		profileID := e2eVSphere.GetSpbmPolicyID(storagePolicyName)
		log.Infof("Profile ID :%s", profileID)
		scParameters := make(map[string]string)
		scParameters["storagePolicyID"] = profileID
		err = client.StorageV1().StorageClasses().Delete(ctx, storagePolicyName, metav1.DeleteOptions{})
		if err != nil {
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		storageclass, err := createStorageClass(client, scParameters, nil, "", "", false, storagePolicyName)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		log.Infof("storageclass name :%s", storageclass.GetName())
		storageclass, err = client.StorageV1().StorageClasses().Get(ctx, storagePolicyName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		log.Infof("storageclass name :%s", storageclass.GetName())

		ginkgo.By("create resource quota")
		createResourceQuota(client, namespace, rqLimit, storagePolicyName)

		return restConfig, storageclass, profileID
	}

	testCleanUpUtil := func(ctx context.Context, restClientConfig *rest.Config, cnsRegistervolumeName string, namespace string, pvcName string, pvName string) {
		ginkgo.By("Deleting the PV Claim")
		framework.ExpectNoError(fpv.DeletePersistentVolumeClaim(client, pvcName, namespace), "Failed to delete PVC ", pvcName)
		pvc = nil

		ginkgo.By("Verify PV should be deleted automatically")
		framework.ExpectNoError(framework.WaitForPersistentVolumeDeleted(client, pvName, poll, pollTimeoutShort))
		pv = nil

		if cnsRegistervolumeName != "" {
			ginkgo.By("Verify CRD should be deleted automatically")
			time.Sleep(time.Duration(40) * time.Second)
			flag := queryCNSRegisterVolume(ctx, restClientConfig, cnsRegistervolumeName, namespace)
			gomega.Expect(flag).NotTo(gomega.BeTrue())
		}

		ginkgo.By("Delete Resource quota")
		deleteResourceQuota(client, namespace)
	}

	/*
		This test verifies the static provisioning workflow.

		Test Steps:
		1. Create FCD and wait for fcd to allow syncing with pandora.
		2. Create PV Spec with volumeID set to FCDID created in Step-1, and PersistentVolumeReclaimPolicy is set to Delete.
		3. Create PVC with the storage request set to PV's storage capacity.
		4. Wait for PV and PVC to bound.
		5. Create a POD.
		6. Verify volume is attached to the node and volume is accessible in the pod.
		7. Verify container volume metadata is present in CNS cache.
		8. Delete POD.
		9. Verify volume is detached from the node.
		10. Delete PVC.
		11. Verify PV is deleted automatically.
	*/
	ginkgo.It("[csi-block-vanilla] Verify basic static provisioning workflow", func() {
		var err error

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ginkgo.By("Creating FCD Disk")
		fcdID, err := e2eVSphere.createFCD(ctx, "BasicStaticFCD", diskSizeInMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deleteFCDRequired = true

		ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow newly created FCD:%s to sync with pandora", pandoraSyncWaitTime, fcdID))
		time.Sleep(time.Duration(pandoraSyncWaitTime) * time.Second)

		// Creating label for PV.
		// PVC will use this label as Selector to find PV
		staticPVLabels := make(map[string]string)
		staticPVLabels["fcd-id"] = fcdID

		ginkgo.By("Creating the PV")
		pv = getPersistentVolumeSpec(fcdID, v1.PersistentVolumeReclaimDelete, staticPVLabels)
		pv, err = client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			return
		}
		err = e2eVSphere.waitForCNSVolumeToBeCreated(pv.Spec.CSI.VolumeHandle)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Creating the PVC")
		pvc = getPersistentVolumeClaimSpec(namespace, staticPVLabels, pv.Name)
		pvc, err = client.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Wait for PV and PVC to Bind
		framework.ExpectNoError(fpv.WaitOnPVandPVC(client, namespace, pv, pvc))

		// Set deleteFCDRequired to false.
		// After PV, PVC is in the bind state, Deleting PVC should delete container volume.
		// So no need to delete FCD directly using vSphere API call.
		deleteFCDRequired = false

		ginkgo.By("Verifying CNS entry is present in cache")
		_, err = e2eVSphere.queryCNSVolumeWithResult(pv.Spec.CSI.VolumeHandle)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Creating the Pod")
		var pvclaims []*v1.PersistentVolumeClaim
		pvclaims = append(pvclaims, pvc)
		pod, err := fpod.CreatePod(client, namespace, nil, pvclaims, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By(fmt.Sprintf("Verify volume: %s is attached to the node: %s", pv.Spec.CSI.VolumeHandle, pod.Spec.NodeName))
		vmUUID := getNodeUUID(client, pod.Spec.NodeName)
		isDiskAttached, err := e2eVSphere.isVolumeAttachedToVM(client, pv.Spec.CSI.VolumeHandle, vmUUID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(isDiskAttached).To(gomega.BeTrue(), "Volume is not attached")

		ginkgo.By("Verify the volume is accessible and available to the pod by creating an empty file")
		filepath := filepath.Join("/mnt/volume1", "/emptyFile.txt")
		_, err = framework.LookForStringInPodExec(namespace, pod.Name, []string{"/bin/touch", filepath}, "", time.Minute)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Verify container volume metadata is present in CNS cache")
		ginkgo.By(fmt.Sprintf("Invoking QueryCNSVolume with VolumeID: %s", pv.Spec.CSI.VolumeHandle))
		_, err = e2eVSphere.queryCNSVolumeWithResult(pv.Spec.CSI.VolumeHandle)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		labels := []types.KeyValue{{Key: "fcd-id", Value: fcdID}}
		ginkgo.By("Verify container volume metadata is matching the one in CNS cache")
		err = verifyVolumeMetadataInCNS(&e2eVSphere, pv.Spec.CSI.VolumeHandle, pvc.Name, pv.ObjectMeta.Name, pod.Name, labels...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Deleting the Pod")
		framework.ExpectNoError(fpod.DeletePodWithWait(client, pod), "Failed to delete pod ", pod.Name)

		ginkgo.By(fmt.Sprintf("Verify volume is detached from the node: %s", pod.Spec.NodeName))
		isDiskDetached, err := e2eVSphere.waitForVolumeDetachedFromNode(client, pv.Spec.CSI.VolumeHandle, pod.Spec.NodeName)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(isDiskDetached).To(gomega.BeTrue(), "Volume is not detached from the node")

		ginkgo.By("Deleting the PV Claim")
		framework.ExpectNoError(fpv.DeletePersistentVolumeClaim(client, pvc.Name, namespace), "Failed to delete PVC ", pvc.Name)
		pvc = nil

		ginkgo.By("Verify PV should be deleted automatically")
		framework.ExpectNoError(framework.WaitForPersistentVolumeDeleted(client, pv.Name, poll, pollTimeout))
		pv = nil
	})
	/*
		This test verifies the static provisioning workflow in guest cluster.

		Test Steps:
		1. Create a PVC using the existing SC in GC
		2. Wait for PVC to be Bound in GC
		3. Verifying if the mapping PVC is bound in SC using the volume handler
		4. Verify volume is created on CNS and check the spbm health
		5. Change the reclaim policy of the PV from delete to retain in GC
		6. Delete PVC in GC
		7. Delete PV in GC
		8. Verifying if PVC and PV still persists in SV cluster
		9. Create a PV with reclaim policy=delete in GC using the bound PVC in SV cluster as the volume id
		10. Create a PVC in GC using the above PV
		11. Verify the PVC is bound in GC.
		12. Delete the PVC in GC
		13. Verifying if PVC and PV also deleted in the SV cluster
		14. Verify volume is deleted on CNS
	*/
	ginkgo.It("[csi-guest] Static provisioning workflow in guest cluster", func() {
		var err error

		storagePolicyNameForSharedDatastores := GetAndExpectStringEnvVar(envStoragePolicyNameForSharedDatastores)
		scParameters := make(map[string]string)
		scParameters[svStorageClassName] = storagePolicyNameForSharedDatastores
		storageclass, pvclaim, err := createPVCAndStorageClass(client, namespace, nil, scParameters, "", nil, "", false, "")

		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		defer func() {
			err := client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, *metav1.NewDeleteOptions(0))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()
		defer func() {
			err := fpv.DeletePersistentVolumeClaim(client, pvclaim.Name, namespace)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Waiting for claim to be in bound phase")
		ginkgo.By("Expect claim to pass provisioning volume as shared datastore")
		err = fpv.WaitForPersistentVolumeClaimPhase(v1.ClaimBound, client, pvclaim.Namespace, pvclaim.Name, framework.Poll, framework.ClaimProvisionTimeout)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		pv := getPvFromClaim(client, pvclaim.Namespace, pvclaim.Name)
		volumeID := pv.Spec.CSI.VolumeHandle
		// svcPVCName refers to PVC Name in the supervisor cluster
		svcPVCName := volumeID
		volumeID = getVolumeIDFromSupervisorCluster(svcPVCName)
		gomega.Expect(volumeID).NotTo(gomega.BeEmpty())

		ginkgo.By(fmt.Sprintf("Invoking QueryCNSVolumeWithResult with VolumeID: %s", volumeID))
		queryResult, err := e2eVSphere.queryCNSVolumeWithResult(volumeID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(queryResult.Volumes).ShouldNot(gomega.BeEmpty())

		pv.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimRetain
		pv, err = client.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Delete the PVC")
		err = fpv.DeletePersistentVolumeClaim(client, pvclaim.Name, namespace)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By(fmt.Sprintf("Delete the PV %s", pv.Name))
		err = client.CoreV1().PersistentVolumes().Delete(ctx, pv.Name, *metav1.NewDeleteOptions(0))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Verifying if volume still exists in the Supervisor Cluster")
		// svcPVCName refers to PVC Name in the supervisor cluster
		volumeID = getVolumeIDFromSupervisorCluster(svcPVCName)
		gomega.Expect(volumeID).NotTo(gomega.BeEmpty())

		// Creating label for PV.
		// PVC will use this label as Selector to find PV
		staticPVLabels := make(map[string]string)
		staticPVLabels["fcd-id"] = volumeID

		ginkgo.By("Creating the PV")
		pv = getPersistentVolumeSpec(svcPVCName, v1.PersistentVolumeReclaimDelete, staticPVLabels)
		pv, err = client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Creating the PVC")
		pvc = getPersistentVolumeClaimSpec(namespace, staticPVLabels, pv.Name)
		pvc, err = client.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Wait for PV and PVC to Bind
		framework.ExpectNoError(fpv.WaitOnPVandPVC(client, namespace, pv, pvc))

		ginkgo.By("Deleting the PV Claim")
		framework.ExpectNoError(fpv.DeletePersistentVolumeClaim(client, pvc.Name, namespace), "Failed to delete PVC ", pvc.Name)
		pvc = nil

		ginkgo.By("Verify PV should be deleted automatically")
		framework.ExpectNoError(framework.WaitForPersistentVolumeDeleted(client, pv.Name, poll, pollTimeoutShort))
		pv = nil

		ginkgo.By("Verify volume is deleted in Supervisor Cluster")
		volumeExists := verifyVolumeExistInSupervisorCluster(svcPVCName)
		gomega.Expect(volumeExists).To(gomega.BeFalse())
	})

	/*
		This test verifies the static provisioning workflow II in guest cluster.

		Test Steps:
		1. Create a PVC using the existing SC in SV Cluster
		2. Wait for PVC to be Bound in SV cluster
		3. Create a PV with reclaim policy=delete in GC using the bound PVC in SV cluster as the volume id
		4. Create a PVC in GC using the above PV
		5. Verify the PVC is bound in GC.
		6. Delete the PVC in GC
		7. Verifying if PVC and PV also deleted in the SV cluster
		8. Verify volume is deleted on CNS
	*/
	ginkgo.It("[csi-guest] Static provisioning workflow II in guest cluster", func() {
		var err error
		storagePolicyNameForSharedDatastores := GetAndExpectStringEnvVar(envStoragePolicyNameForSharedDatastores)

		// create supvervisor cluster client
		var svcClient clientset.Interface
		if k8senv := GetAndExpectStringEnvVar("SUPERVISOR_CLUSTER_KUBE_CONFIG"); k8senv != "" {
			svcClient, err = k8s.CreateKubernetesClientFromConfig(k8senv)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		svNamespace := GetAndExpectStringEnvVar(envSupervisorClusterNamespace)

		ginkgo.By("Creating PVC in supervisor cluster")
		// get storageclass from the supervisor cluster
		storageclass, err := svcClient.StorageV1().StorageClasses().Get(ctx, storagePolicyNameForSharedDatastores, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pvclaim, err := createPVC(svcClient, svNamespace, nil, "", storageclass, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		defer func() {
			err := fpv.DeletePersistentVolumeClaim(svcClient, pvclaim.Name, svNamespace)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Waiting for claim to be in bound phase")
		ginkgo.By("Expect claim to pass provisioning volume as shared datastore")
		err = fpv.WaitForPersistentVolumeClaimPhase(v1.ClaimBound, svcClient, pvclaim.Namespace, pvclaim.Name, framework.Poll, framework.ClaimProvisionTimeout)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		pv := getPvFromClaim(svcClient, pvclaim.Namespace, pvclaim.Name)
		volumeID := pv.Spec.CSI.VolumeHandle

		// Creating label for PV.
		// PVC will use this label as Selector to find PV
		staticPVLabels := make(map[string]string)
		staticPVLabels["fcd-id"] = volumeID

		svcPVCName := volumeID
		ginkgo.By("Creating the PV in guest cluster")
		pv = getPersistentVolumeSpec(svcPVCName, v1.PersistentVolumeReclaimDelete, staticPVLabels)
		pv, err = client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Creating the PVC in guest cluster")
		pvc = getPersistentVolumeClaimSpec(namespace, staticPVLabels, pv.Name)
		pvc, err = client.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Wait for PV and PVC to Bind
		framework.ExpectNoError(fpv.WaitOnPVandPVC(client, namespace, pv, pvc))

		ginkgo.By("Deleting the PV Claim")
		framework.ExpectNoError(fpv.DeletePersistentVolumeClaim(client, pvc.Name, namespace), "Failed to delete PVC ", pvc.Name)
		pvc = nil

		ginkgo.By("Verify PV should be deleted automatically")
		framework.ExpectNoError(framework.WaitForPersistentVolumeDeleted(client, pv.Name, poll, pollTimeoutShort))
		pv = nil

		ginkgo.By("Verify volume is deleted in Supervisor Cluster")
		volumeExists := verifyVolumeExistInSupervisorCluster(svcPVCName)
		gomega.Expect(volumeExists).To(gomega.BeFalse())

		ginkgo.By("Delete Resource quota")
		deleteResourceQuota(client, namespace)
	})

	/*
		This test verifies the static provisioning workflow on supervisor cluster.

		Test Steps:
		1. Create CNS volume note the volumeID
		2. Create Resource quota
		3. create CNS register volume with above created VolumeID
		4. verify created PV, PVC and check the bidirectional reference.
		5. Create POD , with above created PVC
		6. Verify volume is attached to the node and volume is accessible in the pod.
		7. Delete POD.
		8. Delete PVC.
		9. Verify PV is deleted automatically.
		10. Verify Volume id deleted automatically.
		11. Verify CRD deleted automatically
	*/
	ginkgo.It("[csi-supervisor] Verify static provisioning workflow on SVC - import CNS volume", func() {
		var err error
		ctx, cancel := context.WithCancel(context.Background())
		log := logger.GetLogger(ctx)
		defer cancel()

		curtime := time.Now().Unix()
		curtimestring := strconv.FormatInt(curtime, 10)
		pvcName := "cns-pvc-" + curtimestring
		log.Infof("pvc name :%s", pvcName)

		restConfig, storageclass, profileID := staticProvisioningPreSetUpUtil(ctx)

		defer func() {
			log.Infof("Delete storage class")
			err = client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Creating FCD (CNS Volume)")
		fcdID, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd"+curtimestring, profileID, diskSizeInMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deleteFCDRequired = false
		ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow newly created FCD:%s to sync with pandora", pandoraSyncWaitTime, fcdID))
		time.Sleep(time.Duration(pandoraSyncWaitTime) * time.Second)

		ginkgo.By("Create CNS register volume with above created FCD ")
		cnsRegisterVolume := getCNSRegisterVolumeSpec(ctx, namespace, fcdID, pvcName, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(40) * time.Second)
		cnsRegisterVolumeName := cnsRegisterVolume.GetName()
		log.Infof("CNS register volume name : %s", cnsRegisterVolumeName)

		ginkgo.By(" verify created PV, PVC and check the bidirectional reference")
		pvc, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv := getPvFromClaim(client, namespace, pvcName)
		verifyBidirectionalReferenceOfPVandPVC(ctx, client, pvc, pv, fcdID)

		ginkgo.By("Creating pod")
		pod, err := fpod.CreatePod(client, namespace, nil, []*v1.PersistentVolumeClaim{pvc}, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(60) * time.Second)
		podName := pod.GetName
		log.Infof("podName : %s", podName)

		ginkgo.By(fmt.Sprintf("Verify volume: %s is attached to the node: %s", pv.Spec.CSI.VolumeHandle, pod.Spec.NodeName))
		var vmUUID string
		var exists bool
		annotations := pod.Annotations
		vmUUID, exists = annotations[vmUUIDLabel]
		gomega.Expect(exists).To(gomega.BeTrue(), fmt.Sprintf("Pod doesn't have %s annotation", vmUUIDLabel))
		_, err = e2eVSphere.getVMByUUID(ctx, vmUUID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Deleting the pod")
		err = fpod.DeletePodWithWait(client, pod)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By(fmt.Sprintf("Verify volume: %s is detached from PodVM with vmUUID: %s", pv.Spec.CSI.VolumeHandle, vmUUID))
		_, err = e2eVSphere.getVMByUUIDWithWait(ctx, vmUUID, supervisorClusterOperationsTimeout)
		gomega.Expect(err).To(gomega.HaveOccurred(), fmt.Sprintf("PodVM with vmUUID: %s still exists. So volume: %s is not detached from the PodVM", vmUUID, pv.Spec.CSI.VolumeHandle))

		defer func() {
			testCleanUpUtil(ctx, restConfig, cnsRegisterVolumeName, namespace, pvc.Name, pv.Name)
		}()

	})

	/*
		This test verifies the static provisioning workflow on supervisor cluster.

		Test Steps:
		1. Create FCD with valid storage policy.
		2. Create Resource quota
		3. Create CNS register volume with above created FCD
		4. verify PV, PVC got created , check the bidirectional reference.
		5. Create POD , with above created PVC
		6. Verify volume is attached to the node and volume is accessible in the pod.
		7. Delete POD.
		8. Delete PVC.
		9. Verify PV is deleted automatically.
		10. Verify Volume id deleted automatically.
		11. Verify CRD deleted automatically
	*/
	ginkgo.It("[csi-supervisor] Verify static provisioning workflow on SVC import FCD ", func() {

		var err error
		ctx, cancel := context.WithCancel(context.Background())
		log := logger.GetLogger(ctx)
		defer cancel()

		curtime := time.Now().Unix()
		curtimeinstring := strconv.FormatInt(curtime, 10)
		pvcName := "cns-pvc-" + curtimeinstring
		log.Infof("pvc name :%s", pvcName)
		namespace = getNamespaceToRunTests(f)

		restConfig, storageclass, profileID := staticProvisioningPreSetUpUtil(ctx)

		defer func() {
			log.Infof("Delete storage class")
			err = client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Creating FCD Disk")
		fcdID, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd"+curtimeinstring, profileID, diskSizeInMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deleteFCDRequired = false

		ginkgo.By("Create CNS register volume with above created FCD")
		cnsRegisterVolume := getCNSRegisterVolumeSpec(ctx, namespace, fcdID, pvcName, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		log.Infof("waiting for some time for FCD to register in CNS and for cnsRegisterVolume to get create")
		time.Sleep(time.Duration(100) * time.Second)
		cnsRegisterVolumeName := cnsRegisterVolume.GetName()
		log.Infof("CNS register volume name : %s", cnsRegisterVolumeName)

		ginkgo.By("verify created PV, PVC and check the bidirectional reference")
		pvc, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv := getPvFromClaim(client, namespace, pvcName)
		verifyBidirectionalReferenceOfPVandPVC(ctx, client, pvc, pv, fcdID)

		ginkgo.By("Creating pod")
		pod, err := fpod.CreatePod(client, namespace, nil, []*v1.PersistentVolumeClaim{pvc}, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(60) * time.Second)
		podName := pod.GetName
		log.Infof("podName: %s", podName)

		ginkgo.By(fmt.Sprintf("Verify volume: %s is attached to the node: %s", pv.Spec.CSI.VolumeHandle, pod.Spec.NodeName))
		var vmUUID string
		var exists bool
		annotations := pod.Annotations
		vmUUID, exists = annotations[vmUUIDLabel]
		gomega.Expect(exists).To(gomega.BeTrue(), fmt.Sprintf("Pod doesn't have %s annotation", vmUUIDLabel))
		_, err = e2eVSphere.getVMByUUID(ctx, vmUUID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Deleting the pod")
		err = fpod.DeletePodWithWait(client, pod)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By(fmt.Sprintf("Verify volume: %s is detached from PodVM with vmUUID: %s", pv.Spec.CSI.VolumeHandle, vmUUID))
		_, err = e2eVSphere.getVMByUUIDWithWait(ctx, vmUUID, supervisorClusterOperationsTimeout)
		gomega.Expect(err).To(gomega.HaveOccurred(), fmt.Sprintf("PodVM with vmUUID: %s still exists. So volume: %s is not detached from the PodVM", vmUUID, pv.Spec.CSI.VolumeHandle))

		defer func() {
			testCleanUpUtil(ctx, restConfig, cnsRegisterVolumeName, namespace, pvc.Name, pv.Name)
		}()

	})

	/*
		This test verifies the static provisioning workflow on supervisor cluster when there is no resource quota available.

		Test Steps:
		1. Create FCD with valid storage policy.
		2. Delete the existing resource quota
		3. create CNS register volume with above created FCD
		4. Since there is no resource quota available , CRD will be in pending state
		5. Verify  PVC creation fails
		6. Increase Resource quota
		7. verify PVC, PV got created , check the bidirectional reference.
		8. Create POD , with above created PVC
		9. Verify volume is attached to the node and volume is accessible in the pod.
		10. Delete POD.
		11. Delete PVC.
		12. Verify PV is deleted automatically.
		13. Verify Volume id deleted automatically.
		14. Verify CRD deleted automatically
	*/
	ginkgo.It("[csi-supervisor] Verify static provisioning workflow on svc - when there is no resourcequota available ", func() {

		var err error
		ctx, cancel := context.WithCancel(context.Background())
		log := logger.GetLogger(ctx)
		defer cancel()

		curtime := time.Now().Unix()
		curtimeinstring := strconv.FormatInt(curtime, 10)
		pvcName := "cns-pvc-" + curtimeinstring

		restConfig, storageclass, profileID := staticProvisioningPreSetUpUtil(ctx)

		defer func() {
			log.Infof("Delete storage clas")
			err = client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Create FCD with valid storage policy.")
		fcdID, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd"+curtimeinstring, profileID, diskSizeInMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deleteFCDRequired = false

		ginkgo.By("Delete existing resource quota")
		deleteResourceQuota(client, namespace)

		ginkgo.By("Import above created FCD")
		cnsRegisterVolume := getCNSRegisterVolumeSpec(ctx, namespace, fcdID, pvcName, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)

		ginkgo.By("Since there is no resource quota available, Verify  PVC creation fails")
		_, err = client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		gomega.Expect(err).To(gomega.HaveOccurred())

		ginkgo.By("Create resource quota")
		createResourceQuota(client, namespace, rqLimit, storagePolicyName)
		log.Infof("Wait till the PVC creation succeeds after increasing resource quota")
		time.Sleep(time.Duration(120) * time.Second)
		cnsRegisterVolumeName := cnsRegisterVolume.GetName()
		log.Infof("CNS register volume name : %s", cnsRegisterVolume)

		ginkgo.By(" verify created PV, PVC and check the bidirectional reference")
		pvc, err = client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv := getPvFromClaim(client, namespace, pvcName)
		verifyBidirectionalReferenceOfPVandPVC(ctx, client, pvc, pv, fcdID)

		ginkgo.By("Creating pod")
		pod, err := fpod.CreatePod(client, namespace, nil, []*v1.PersistentVolumeClaim{pvc}, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(60) * time.Second)
		podName := pod.GetName
		log.Infof("podName: %s", podName)

		ginkgo.By(fmt.Sprintf("Verify volume: %s is attached to the node: %s", pv.Spec.CSI.VolumeHandle, pod.Spec.NodeName))
		var vmUUID string
		var exists bool
		annotations := pod.Annotations
		vmUUID, exists = annotations[vmUUIDLabel]
		gomega.Expect(exists).To(gomega.BeTrue(), fmt.Sprintf("Pod doesn't have %s annotation", vmUUIDLabel))
		_, err = e2eVSphere.getVMByUUID(ctx, vmUUID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Deleting the pod")
		err = fpod.DeletePodWithWait(client, pod)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.By("Wait for 3 minutes for the pod to get terminated successfully")
		time.Sleep(supervisorClusterOperationsTimeout)
		ginkgo.By(fmt.Sprintf("Verify volume: %s is detached from PodVM with vmUUID: %s", pv.Spec.CSI.VolumeHandle, vmUUID))
		_, err = e2eVSphere.getVMByUUIDWithWait(ctx, vmUUID, supervisorClusterOperationsTimeout)
		gomega.Expect(err).To(gomega.HaveOccurred(), fmt.Sprintf("PodVM with vmUUID: %s still exists. So volume: %s is not detached from the PodVM", vmUUID, pv.Spec.CSI.VolumeHandle))
		defer func() {
			testCleanUpUtil(ctx, restConfig, cnsRegisterVolumeName, namespace, pvc.Name, pv.Name)
		}()
	})

	/*
		This test verifies the static provisioning workflow on supervisor cluster - AccessMode is ReadWriteMany / ReadOnlyMany.

		Test Steps:
		1. Create FCD with valid storage policy.
		2. Create Resource quota
		3. Create CNS register volume with above created FCD, AccessMode as "ReadWriteMany"
		4. verify  the error message
		5. Create CNS register volume with above created FCD, AccessMode as "ReadOnlyMany"
		6. verify  the error message
		7. Delete Resource quota
	*/
	ginkgo.It("[csi-supervisor] Verify static volume provisioning when AccessMode is ReadWriteMany or ReadOnlyMany", func() {
		var err error
		ctx, cancel := context.WithCancel(context.Background())
		log := logger.GetLogger(ctx)
		defer cancel()

		curtime := time.Now().Unix()
		curtimeinstring := strconv.FormatInt(curtime, 10)
		pvcName := "cns-pvc-" + curtimeinstring
		framework.Logf("pvc name :%s", pvcName)

		restConfig, storageclass, _ := staticProvisioningPreSetUpUtil(ctx)

		defer func() {
			framework.Logf("Delete storage class ")
			err = client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Create CNS register volume with above created FCD , AccessMode is set to ReadWriteMany ")
		cnsRegisterVolume := getCNSRegisterVolumeSpec(ctx, namespace, fcdID, pvcName, v1.ReadWriteMany)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		time.Sleep(time.Duration(60) * time.Second)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Verify error message when AccessMode is set to ReadWriteMany")
		cnsRegisterVolume = getCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		actualErrorMsg := cnsRegisterVolume.Status.Error
		expectedErrorMsg := "AccessMode: ReadWriteMany is not supported"
		if actualErrorMsg != expectedErrorMsg {
			log.Errorf("Expected error message : ", expectedErrorMsg)
			log.Errorf("Actual error message : ", actualErrorMsg)
			gomega.Expect(actualErrorMsg).NotTo(gomega.HaveOccurred())
		}

		ginkgo.By("Create CNS register volume with above created FCD ,AccessMode is set to ReadOnlyMany")
		cnsRegisterVolume = getCNSRegisterVolumeSpec(ctx, namespace, fcdID, pvcName, v1.ReadOnlyMany)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		time.Sleep(time.Duration(40) * time.Second)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Verify error message when AccessMode is set to ReadOnlyMany")
		cnsRegisterVolume = getCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		actualErrorMsg = cnsRegisterVolume.Status.Error
		expectedErrorMsg = "AccessMode: ReadOnlyMany is not supported"
		if actualErrorMsg != expectedErrorMsg {
			log.Errorf("Expected error message : ", expectedErrorMsg)
			log.Errorf("Actual error message : ", actualErrorMsg)
			gomega.Expect(actualErrorMsg).NotTo(gomega.HaveOccurred())
		}

	})

	/*
		This test verifies the static provisioning workflow on supervisor cluster, When duplicate FCD is used.

		Test Steps:
		1. Create FCD with valid storage policy.
		2. Create Resource quota
		3. Create CNS register volume with above created FCD
		4. verify PV, PVC got created.
		5. Use the above created FCD and create CNS register volume.
		6. Verify the error message
		7. Edit CRD with new FCDID
		8. Verify newly created  PV , PVC and verify the bidirectional reference
		7. Delete PVC.
		8. Verify PV is deleted automatically.
		9. Verify Volume id deleted automatically.
		10. Verify CRD deleted automatically
	*/
	ginkgo.It("[csi-supervisor] Verify static provisioning workflow - when DuplicateFCD is used", func() {

		var err error
		ctx, cancel := context.WithCancel(context.Background())
		log := logger.GetLogger(ctx)
		defer cancel()

		curtime := time.Now().Unix()
		curtimeinstring := strconv.FormatInt(curtime, 10)
		pvcName := "cns-pvc-" + curtimeinstring
		log.Infof("pvc name :%s", pvcName)

		restConfig, storageclass, profileID := staticProvisioningPreSetUpUtil(ctx)

		defer func() {
			log.Infof("Delete storage class ")
			err = client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Creating Resource quota")
		createResourceQuota(client, namespace, rqLimit, storagePolicyName)

		ginkgo.By("Create FCD")
		fcdID1, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd1"+curtimeinstring, profileID, diskSizeInMinMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fcdID2, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd2"+curtimeinstring, profileID, diskSizeInMinMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deleteFCDRequired = false
		ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow newly created FCD:%s to sync with pandora", pandoraSyncWaitTime, fcdID))
		time.Sleep(time.Duration(pandoraSyncWaitTime) * time.Second)

		ginkgo.By("Create CNS register volume with above created FCD ")
		cnsRegisterVolume := getCNSRegisterVolumeSpec(ctx, namespace, fcdID1, pvcName, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(60) * time.Second)
		cnsRegisterVolumeName := cnsRegisterVolume.GetName()
		log.Infof("CNS register volume name : %s", cnsRegisterVolumeName)

		ginkgo.By("verify created PV, PVC ")
		pvc1, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv1 := getPvFromClaim(client, namespace, pvcName)

		ginkgo.By("Create CnsregisteVolume with already used FCD")
		pvcName2 := pvcName + "duplicatefcd"
		cnsRegisterVolume = getCNSRegisterVolumeSpec(ctx, namespace, fcdID1, pvcName2, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(40) * time.Second)

		ginkgo.By("Verify the error message, when already used FCD is used")
		cnsRegisterVolume = getCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		actualErrorMsg := cnsRegisterVolume.Status.Error
		expectedErrorMsg := "Duplicate Request"
		if actualErrorMsg != expectedErrorMsg {
			log.Errorf("Expected error message : ", expectedErrorMsg)
			log.Errorf("Actual error message : ", actualErrorMsg)
			gomega.Expect(actualErrorMsg).NotTo(gomega.HaveOccurred())
		}

		ginkgo.By("Update CRD with new FCD ID")
		cnsRegisterVolume.Spec.VolumeID = fcdID2
		cnsRegisterVolume = updateCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		cnsRegisterVolume = getCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		log.Infof("PVC name after updating the FCDID  :%s", cnsRegisterVolume.Spec.PvcName)

		ginkgo.By("Wait for some time for the updated CRD to create PV , PVC")
		time.Sleep(time.Duration(60) * time.Second)

		ginkgo.By("verify newly created PV, PVC")
		pvc2, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName2, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv2 := getPvFromClaim(client, namespace, pvcName2)
		verifyBidirectionalReferenceOfPVandPVC(ctx, client, pvc2, pv2, fcdID2)

		defer func() {
			ginkgo.By("Deleting the PV Claim")
			framework.ExpectNoError(fpv.DeletePersistentVolumeClaim(client, pvc2.Name, namespace), "Failed to delete PVC ", pvc2.Name)

			ginkgo.By("Verify PV should be deleted automatically")
			framework.ExpectNoError(framework.WaitForPersistentVolumeDeleted(client, pv2.Name, poll, pollTimeoutShort))

			testCleanUpUtil(ctx, restConfig, "", namespace, pvc1.Name, pv1.Name)
		}()

	})

	/*
		This test verifies the static provisioning workflow on supervisor cluster - when duplicate PVC name is used.

		Test Steps:
		1. Create FCD with valid storage policy.
		2. Create Resource quota
		3. Create CNS register volume with above created FCD
		4. verify PV, PVC got created.
		5. Create CNS register volume with new FCD, but already created PVC name
		6. Verify the error message, due to duplicate PVC name.
		7. Edit CRD with new PVC name
		8. Verify newly cretaed PV, PVC and bidirectional references
		9. Delete PVC
		10. Verify PV is deleted automatically.
		11. Verify Volume id deleted automatically.
		12. Verify CRD deleted automatically

	*/
	ginkgo.It("[csi-supervisor] Verify static provisioning workflow - when DuplicatePVC name is used", func() {

		var err error
		ctx, cancel := context.WithCancel(context.Background())
		log := logger.GetLogger(ctx)
		defer cancel()

		curtime := time.Now().Unix()
		curtimeinstring := strconv.FormatInt(curtime, 10)
		pvcName := "cns-pvc-" + curtimeinstring
		log.Infof("pvc name :%s", pvcName)

		restConfig, storageclass, profileID := staticProvisioningPreSetUpUtil(ctx)

		defer func() {
			log.Infof("Delete storage class")
			err = client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Create FCD")
		fcdID1, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd1"+curtimeinstring, profileID, diskSizeInMinMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fcdID2, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd2"+curtimeinstring, profileID, diskSizeInMinMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow newly created FCD:%s to sync with pandora", pandoraSyncWaitTime, fcdID))
		time.Sleep(time.Duration(pandoraSyncWaitTime) * time.Second)
		deleteFCDRequired = false

		ginkgo.By("Create CNS register volume with above created FCD ")
		cnsRegisterVolume := getCNSRegisterVolumeSpec(ctx, namespace, fcdID1, pvcName, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(60) * time.Second)
		cnsRegisterVolumeName := cnsRegisterVolume.GetName()
		log.Infof("CNS register volume name : %s", cnsRegisterVolumeName)

		ginkgo.By("verify created PV, PVC")
		pvc1, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv1 := getPvFromClaim(client, namespace, pvcName)

		ginkgo.By("Create CnsregisteVolume with already created PVC")
		cnsRegisterVolume = getCNSRegisterVolumeSpec(ctx, namespace, fcdID2, pvcName, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(40) * time.Second)

		ginkgo.By("Verify Error message when duplicate PVC is used")
		cnsRegisterVolume = getCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		actualErrorMsg := cnsRegisterVolume.Status.Error
		expectedErrorMsg := "Another PVC: " + pvcName + " already exists in namespace: " + namespace + " which is Bound to a different PV"
		if actualErrorMsg != expectedErrorMsg {
			log.Errorf("Expected error message : ", expectedErrorMsg)
			log.Errorf("Actual error message : ", actualErrorMsg)
			gomega.Expect(actualErrorMsg).NotTo(gomega.HaveOccurred())
		}

		updatedpvcName := "unique-pvc" + curtimeinstring
		cnsRegisterVolume.Spec.PvcName = updatedpvcName
		cnsRegisterVolume = updateCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		cnsRegisterVolume = getCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		log.Infof("Updated pvc name  :%s", cnsRegisterVolume.Spec.PvcName)

		ginkgo.By("Wait for some time for the updated CRD to create PV , PVC")
		time.Sleep(time.Duration(100) * time.Second)

		ginkgo.By("verify created PV, PVC")
		pvc2, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, updatedpvcName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv2 := getPvFromClaim(client, namespace, updatedpvcName)
		verifyBidirectionalReferenceOfPVandPVC(ctx, client, pvc2, pv2, fcdID2)

		defer func() {
			ginkgo.By("Deleting the PV Claim")
			framework.ExpectNoError(fpv.DeletePersistentVolumeClaim(client, pvc2.Name, namespace), "Failed to delete PVC ", pvc2.Name)

			ginkgo.By("Verify PV should be deleted automatically")
			framework.ExpectNoError(framework.WaitForPersistentVolumeDeleted(client, pv2.Name, poll, pollTimeoutShort))

			testCleanUpUtil(ctx, restConfig, "", namespace, pvc1.Name, pv1.Name)
		}()

	})

	/*
				This test verifies the static provisioning workflow on supervisor cluster - When vsanhealthService is down.

				Test Steps:
				1. Create FCD with valid non shared storage policy.
				2. Create Resource quota
				3. Stop VsanHealthService
				4. Create CNS register volume with above created FCD
				5. Verify the error message , since VsanHealthService is down
				6. Start VsanHealthService
				7. CRD should be successful, PV, PVC should get created
				8. Delete PVC
		        9. PV and CRD gets auto deleted
				10. Delete Resource quota
	*/
	ginkgo.It("[csi-supervisor] Verifies static provisioning workflow on supervisor cluster - When vsanhealthService is down", func() {

		var err error
		ctx, cancel := context.WithCancel(context.Background())
		log := logger.GetLogger(ctx)
		defer cancel()

		curtime := time.Now().Unix()
		curtimeinstring := strconv.FormatInt(curtime, 10)
		pvcName := "cns-pvc-" + curtimeinstring
		log.Infof("pvc name :%s", pvcName)

		restConfig, storageclass, profileID := staticProvisioningPreSetUpUtil(ctx)

		defer func() {
			log.Infof("Delete storage class")
			err = client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Creating Resource quota")
		createResourceQuota(client, namespace, rqLimit, storagePolicyName)

		ginkgo.By("Create FCD")
		fcdID, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd"+curtimeinstring, profileID, diskSizeInMinMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By(fmt.Sprintln("Stopping vsan-health on the vCenter host"))
		isVsanhealthServiceStopped = true
		vcAddress := e2eVSphere.Config.Global.VCenterHostname + ":" + sshdPort
		err = invokeVCenterServiceControl("stop", vsanhealthServiceName, vcAddress)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow vsan-health to completely shutdown", vsanHealthServiceWaitTime))
		time.Sleep(time.Duration(vsanHealthServiceWaitTime) * time.Second)

		ginkgo.By("Create CNS register volume with above created FCD")
		cnsRegisterVolume := getCNSRegisterVolumeSpec(ctx, namespace, fcdID, pvcName, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(20) * time.Second)
		cnsRegisterVolumeName := cnsRegisterVolume.GetName()
		log.Infof("CNS register volume name : %s", cnsRegisterVolumeName)

		ginkgo.By("Verify the error message, when vsanhealth is down")
		cnsRegisterVolume = getCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		actualErrorMsg := cnsRegisterVolume.Status.Error
		expectedErrorMsg := "Unable to find the volume in CNS"
		if actualErrorMsg != expectedErrorMsg {
			log.Errorf("Expected error message : ", expectedErrorMsg)
			log.Errorf("Actual error message : ", actualErrorMsg)
			gomega.Expect(actualErrorMsg).NotTo(gomega.HaveOccurred())
		}

		ginkgo.By(fmt.Sprintln("Starting vsan-health on the vCenter host"))
		err = invokeVCenterServiceControl("start", vsanhealthServiceName, vcAddress)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow vsan-health to come up again", vsanHealthServiceWaitTime))
		time.Sleep(time.Duration(vsanHealthServiceWaitTime) * time.Second)

		ginkgo.By("Wait for some time for the CRD to create PV , PVC")
		time.Sleep(time.Duration(60) * time.Second)

		ginkgo.By("verify created PV, PVC")
		pvc, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv := getPvFromClaim(client, namespace, pvcName)
		verifyBidirectionalReferenceOfPVandPVC(ctx, client, pvc, pv, fcdID)

		defer func() {
			testCleanUpUtil(ctx, restConfig, "", namespace, pvc.Name, pv.Name)
		}()

	})

	/*
		This test verifies the static provisioning workflow on supervisor cluster - When SPSService is down.

				Test Steps:
				1. Create FCD with valid non shared storage policy.
				2. Create Resource quota
				3. Stop SPSService
				4. Create CNS register volume with above created FCD
				5. Verify the error message , since SPSService is down
				6. Start SPSService
				7. CRD should be successful, PV, PVC should get created
				8. Delete PVC
		        9. PV and CRD gets auto deleted
				10. Delete Resource quota
	*/
	ginkgo.It("[csi-supervisor] Verifies static provisioning workflow on SVC - When SPS service is down", func() {

		var err error
		ctx, cancel := context.WithCancel(context.Background())
		log := logger.GetLogger(ctx)
		defer cancel()

		curtime := time.Now().Unix()
		curtimeinstring := strconv.FormatInt(curtime, 10)
		pvcName := "cns-pvc-" + curtimeinstring
		log.Infof("pvc name :%s", pvcName)

		restConfig, storageclass, profileID := staticProvisioningPreSetUpUtil(ctx)

		defer func() {
			log.Infof("Delete storage class")
			err = client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name, metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Creating Resource quota")
		createResourceQuota(client, namespace, rqLimit, storagePolicyName)

		ginkgo.By("Create FCD")
		fcdID, err := e2eVSphere.createFCDwithValidProfileID(ctx, "staticfcd"+curtimeinstring, profileID, diskSizeInMinMb, defaultDatastore.Reference())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By(fmt.Sprintln("Stopping sps on the vCenter host"))
		isSPSserviceStopped = true
		vcAddress := e2eVSphere.Config.Global.VCenterHostname + ":" + sshdPort
		err = invokeVCenterServiceControl("stop", "sps", vcAddress)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow sps to completely shutdown", vsanHealthServiceWaitTime))
		time.Sleep(time.Duration(vsanHealthServiceWaitTime) * time.Second)

		ginkgo.By("Create CNS register volume with above created FCD ")
		cnsRegisterVolume := getCNSRegisterVolumeSpec(ctx, namespace, fcdID, pvcName, v1.ReadWriteOnce)
		err = createCNSRegisterVolume(ctx, restConfig, cnsRegisterVolume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		time.Sleep(time.Duration(20) * time.Second)
		cnsRegisterVolumeName := cnsRegisterVolume.GetName()
		log.Infof("CNS register volume name : %s", cnsRegisterVolumeName)

		ginkgo.By("Verify the error message, when SPSService is down, CRD should not be successful")
		cnsRegisterVolume = getCNSRegistervolume(ctx, restConfig, cnsRegisterVolume)
		actualErrorMsg := cnsRegisterVolume.Status.Error
		expectedErrorMsg := "CNS Volume: " + fcdID + " not found"
		if actualErrorMsg != expectedErrorMsg {
			log.Errorf("Expected error message : ", expectedErrorMsg)
			log.Errorf("Actual error message : ", actualErrorMsg)
			gomega.Expect(actualErrorMsg).NotTo(gomega.HaveOccurred())
		}

		ginkgo.By(fmt.Sprintln("Starting sps on the vCenter host"))
		err = invokeVCenterServiceControl("start", "sps", vcAddress)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.By(fmt.Sprintf("Sleeping for %v seconds to allow sps to come up again", vsanHealthServiceWaitTime))
		time.Sleep(time.Duration(vsanHealthServiceWaitTime) * time.Second)

		ginkgo.By("Wait for some time for the updated CRD to create PV , PVC")
		time.Sleep(time.Duration(60) * time.Second)

		ginkgo.By("verify created PV, PVC")
		pvc, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pv := getPvFromClaim(client, namespace, pvcName)
		verifyBidirectionalReferenceOfPVandPVC(ctx, client, pvc, pv, fcdID)

		defer func() {
			testCleanUpUtil(ctx, restConfig, "", namespace, pvc.Name, pv.Name)
		}()
	})

})
