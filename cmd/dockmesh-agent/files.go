package main

import (
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/agents"
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
