package main

import (
	"errors"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/gorilla/websocket"
)

// runImagePullStream pulls an image and relays Docker's progress lines. A
// pull that fails part-way is one of those lines; the server reads it.
func runImagePullStream(conn *websocket.Conn, cli client.ImageAPIClient, open agents.StreamOpen, reg *streamReg) error {
	ref, _ := open.Params["image"].(string)
	auth, _ := open.Params["registry_auth"].(string)
	if ref == "" {
		return errors.New("image required")
	}
	body, err := cli.ImagePull(reg.ctx, ref, image.PullOptions{RegistryAuth: auth})
	if err != nil {
		return errors.New(operations.MaskText(err.Error(), operations.RegistryAuthSecrets(auth)))
	}
	defer body.Close()
	return reg.pumpOutput(conn, body, 32*1024)
}

// runImageExportStream sends an image as a `docker save` archive.
func runImageExportStream(conn *websocket.Conn, cli client.ImageAPIClient, open agents.StreamOpen, reg *streamReg) error {
	ref, _ := open.Params["image"].(string)
	if ref == "" {
		return errors.New("image required")
	}
	body, err := cli.ImageSave(reg.ctx, []string{ref})
	if err != nil {
		return err
	}
	defer body.Close()
	return reg.pumpOutput(conn, body, 32*1024)
}
