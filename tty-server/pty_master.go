package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yi-Tseng/tty-share/common"
	ttyCommon "github.com/Yi-Tseng/tty-share/common"
	ptyDevice "github.com/creack/pty"
	"golang.org/x/crypto/ssh/terminal"
)

// This defines a PTY Master whih will encapsulate the command we want to run, and provide simple
// access to the command, to write and read IO, but also to control the window size.
type ptyMaster struct {
	sessionID    string
	ptyFile      *os.File
	command      *exec.Cmd
	rcvProtoConn *ttyCommon.TTYProtocolConn
}

func ptyMasterNew(sessionID string) *ptyMaster {
	return &ptyMaster{
		sessionID: sessionID,
	}
}

func (pty *ptyMaster) GetSessionID() string {
	return pty.sessionID
}

func (pty *ptyMaster) Start(command string, args []string) (err error) {
	pty.command = exec.Command(command, args...)
	pty.ptyFile, err = ptyDevice.Start(pty.command)

	if err != nil {
		return
	}

	// Set the initial window size
	cols, rows, err := terminal.GetSize(0)
	pty.SetWinSize(rows, cols)
	return
}

func (pty *ptyMaster) GetWinSize() (int, int, error) {
	cols, rows, err := terminal.GetSize(0)
	return cols, rows, err
}

func (pty *ptyMaster) Write(b []byte) (int, error) {
	return pty.ptyFile.Write(b)
}

func (pty *ptyMaster) Read(b []byte) (int, error) {
	return pty.ptyFile.Read(b)
}

func (pty *ptyMaster) SetWinSize(rows, cols int) {
	// ptyDevice.Setsize(pty.ptyFile, rows, cols)

	ws := &ptyDevice.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	}

	ptyDevice.Setsize(pty.ptyFile, ws)
}

func (pty *ptyMaster) Refresh() {
	// We wanna force the app to re-draw itself, but there doesn't seem to be a way to do that
	// so we fake it by resizing the window quickly, making it smaller and then back big
	cols, rows, err := pty.GetWinSize()

	if err != nil {
		return
	}

	pty.SetWinSize(rows-1, cols)

	go func() {
		time.Sleep(time.Millisecond * 50)
		pty.SetWinSize(rows, cols)
	}()
}

func (pty *ptyMaster) Wait() (err error) {
	err = pty.command.Wait()
	if pty.rcvProtoConn != nil {
		err = pty.rcvProtoConn.Close()
	}
	return
}

func (pty *ptyMaster) Stop() (err error) {
	signal.Ignore(syscall.SIGWINCH)

	pty.command.Process.Signal(syscall.SIGTERM)
	// TODO: Find a proper way to close the running command. Perhaps have a timeout after which,
	// if the command hasn't reacted to SIGTERM, then send a SIGKILL
	// (bash for example doesn't finish if only a SIGTERM has been sent)
	pty.command.Process.Signal(syscall.SIGKILL)
	return
}

func (pty *ptyMaster) HandleReceiver(rawConn *WSConnection) {
	if pty.rcvProtoConn != nil {
		// Someone is using this session, reject this request.
		rawConn.Close()
		return
	}
	pty.rcvProtoConn = ttyCommon.NewTTYProtocolConn(rawConn)
	log.Debugf("Got new TTYReceiver connection (%s). Serving it..", rawConn.Address())

	go func() {
		_, err := io.Copy(pty.rcvProtoConn, pty)
		if err != nil {
			log.Debugf("Lost connection: %s\n", err.Error())
		}
	}()

	pty.Refresh()

	for {
		msg, err := pty.rcvProtoConn.ReadMessage()

		if err != nil {
			log.Warnf("Finishing handling the TTYReceiver loop because: %s", err.Error())
			break
		}

		switch msg.Type {
		case ttyCommon.MsgIDWinSize:
			var msgWinSize common.MsgTTYWinSize
			json.Unmarshal(msg.Data, &msgWinSize)
			pty.SetWinSize(msgWinSize.Rows, msgWinSize.Cols)
		case ttyCommon.MsgIDWrite:
			var msgWrite common.MsgTTYWrite
			json.Unmarshal(msg.Data, &msgWrite)
			pty.Write(msgWrite.Data[:msgWrite.Size])
		default:
			log.Warnf("Receiving unknown data from the receiver")
		}
	}

	pty.rcvProtoConn.Close()
	pty.rcvProtoConn = nil
	pty.Stop()
}
