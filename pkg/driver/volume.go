package driver

import (
	"context"
	"fmt"
	"os"

	"github.com/chrislusf/seaweedfs/weed/glog"
	"github.com/chrislusf/seaweedfs/weed/pb/mount_pb"
	"github.com/chrislusf/seaweedfs/weed/util"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Volume struct {
	VolumeId string

	// volume's real mount point
	stagingTargetPath string

	// Target paths to which the volume has been published.
	// These paths are symbolic links to the real mount point.
	// So multiple pods using the same volume can share a mount.
	targetPaths map[string]bool

	mounter Mounter

	// unix socket used to manage volume
	localSocket string
}

func NewVolume(volumeID string, mounter Mounter) *Volume {
	return &Volume{
		VolumeId:    volumeID,
		mounter:     mounter,
		targetPaths: make(map[string]bool),
	}
}

func (vol *Volume) Stage(stagingTargetPath string) error {
	if vol.isStaged() {
		return nil
	}

	// check whether it can be mounted
	if notMnt, err := checkMount(stagingTargetPath); err != nil {
		return err
	} else if !notMnt {
		// maybe already mounted?
		return nil
	}

	if err := vol.mounter.Mount(stagingTargetPath); err != nil {
		return err
	}

	vol.stagingTargetPath = stagingTargetPath
	return nil
}

func (vol *Volume) Publish(targetPath string) error {
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	if err := os.Symlink(vol.stagingTargetPath, targetPath); err != nil {
		return err
	}

	vol.targetPaths[targetPath] = true
	return nil
}

func (vol *Volume) Expand(sizeByte int64) error {
	if !vol.isStaged() {
		return nil
	}

	target := fmt.Sprintf("passthrough:///unix://%s", vol.getLocalSocket())
	dialOption := grpc.WithTransportCredentials(insecure.NewCredentials())

	clientConn, err := grpc.Dial(target, dialOption)
	if err != nil {
		return err
	}
	defer clientConn.Close()

	client := mount_pb.NewSeaweedMountClient(clientConn)
	_, err = client.Configure(context.Background(), &mount_pb.ConfigureRequest{
		CollectionCapacity: sizeByte,
	})
	return err
}

func (vol *Volume) Unpublish(targetPath string) error {
	// Check whether the volume is published to the target path.
	if _, ok := vol.targetPaths[targetPath]; !ok {
		glog.Warningf("volume %s hasn't been published to %s", vol.VolumeId, targetPath)
		return nil
	}

	delete(vol.targetPaths, targetPath)

	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (vol *Volume) Unstage(_ string) error {
	if !vol.isStaged() {
		return nil
	}

	mountPoint := vol.stagingTargetPath
	glog.V(0).Infof("unmounting volume %s from %s", vol.VolumeId, mountPoint)

	if err := fuseUnmount(mountPoint); err != nil {
		return err
	}

	if err := os.Remove(mountPoint); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (vol *Volume) isStaged() bool {
	return vol.stagingTargetPath != ""
}

func (vol *Volume) getLocalSocket() string {
	if vol.localSocket != "" {
		return vol.localSocket
	}

	montDirHash := util.HashToInt32([]byte(vol.VolumeId))
	if montDirHash < 0 {
		montDirHash = -montDirHash
	}

	socket := fmt.Sprintf("/tmp/seaweedfs-mount-%d.sock", montDirHash)
	vol.localSocket = socket
	return socket
}
