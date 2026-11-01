package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/host"
)

// serveContainerFile answers one req.container.files.* frame with the
// same containerfs functions the server uses for its own daemon, so the
// path rules and size limits are identical on every host.
func serveContainerFile(ctx context.Context, cli client.ContainerAPIClient, f agents.Frame) (any, error) {
	var req agents.ContainerFileReq
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return nil, err
	}
	switch f.Type {
	case agents.FrameReqContainerFilesList:
		entries, err := host.BrowseContainerDir(ctx, cli, req.Container, req.Path)
		if entries == nil {
			entries = []host.VolumeEntry{}
		}
		return entries, err
	case agents.FrameReqContainerFilesRead:
		return host.ReadContainerFile(ctx, cli, req.Container, req.Path, req.MaxBytes)
	case agents.FrameReqContainerFilesStat:
		return host.StatContainerFile(ctx, cli, req.Container, req.Path)
	case agents.FrameReqContainerFilesWrite:
		return struct{}{}, host.WriteContainerFile(ctx, cli, req.Container, req.Path, req.Data, req.Mode)
	case agents.FrameReqContainerFilesDelete:
		return struct{}{}, host.DeleteContainerFile(ctx, cli, req.Container, req.Path)
	}
	return nil, fmt.Errorf("unknown request type: %s", f.Type)
}

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
