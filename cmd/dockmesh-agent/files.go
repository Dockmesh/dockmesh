package main

import (
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/agents"
	dockerwrap "github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/host"
)

// runContainerFileStream sends one file's content as stream data.
func runContainerFileStream(conn *websocket.Conn, cli client.ContainerAPIClient, open agents.StreamOpen, reg *streamReg) error {
	p, _ := open.Params["path"].(string)
	body, _, err := host.DownloadContainerFile(reg.ctx, cli, open.Container, p)
	if err != nil {
		return err
	}
	defer body.Close()
	return reg.pumpOutput(conn, body, 32*1024)
}

// runVolumeFileStream sends one whole file inside a volume as stream data.
func runVolumeFileStream(conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) error {
	volName, _ := open.Params["volume"].(string)
	p, _ := open.Params["path"].(string)
	if err := dockerwrap.CheckVolumeName(volName); err != nil {
		return err
	}
	vol, err := cli.VolumeInspect(reg.ctx, volName)
	if err != nil {
		return err
	}
	mp, err := host.ExtractMountpoint(vol.Mountpoint)
	if err != nil {
		return err
	}
	body, err := host.OpenVolumeFileOnHost(reg.ctx, cli, volName, mp, p)
	if err != nil {
		return err
	}
	defer body.Close()
	return reg.pumpOutput(conn, body, 32*1024)
}
