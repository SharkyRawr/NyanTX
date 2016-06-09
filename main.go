package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net"
	"net/http"
	"time"

	"github.com/tjgq/broadcast"
	"golang.org/x/net/websocket"

	"github.com/btcsuite/btcd/wire"

	"github.com/schumann2k/jsoncfg"
)

const (
	connectNode string = "nyanseed.com:33701" //"127.0.0.1:33701" //"nyan.beholder.ml:33701"

	pver     uint32          = 600001
	btcnet   wire.BitcoinNet = 0xddb7d9fc //0xfcd9b7dd in network-endian
	services uint64          = 1
)

type nyanBlock struct {
	Hash  string
	Value float64
	TS    time.Time
	NumTx int
}

var (
	blockBroadcast *broadcast.Broadcaster
	lastBlock      nyanBlock
	cfg            *jsoncfg.JsonConfig
)

func fatalerr(err error) {
	if err != nil {
		log.Fatalf("Fatal Error: %s\n", err)
	}
}

func toJson(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	return string(data), err
}

func listenForTx(conn net.Conn) {
	for {
		msg, _, err := wire.ReadMessage(conn, pver, btcnet)
		if err != nil {
			log.Fatalf("Error reading btcnet message: %s from peer %s", err.Error(), conn.RemoteAddr().String())
		}

		switch msg := msg.(type) {
		case *wire.MsgVerAck:
			{
				log.Printf("Got verack from %s\n", conn.RemoteAddr().String())

				log.Printf("Sending getblocks ...\n")
				hash, err := wire.NewShaHashFromStr(lastBlock.Hash)
				fatalerr(err)
				getblocks := wire.NewMsgGetBlocks(hash)
				getblocks.AddBlockLocatorHash(hash)
				wire.WriteMessage(conn, getblocks, pver, btcnet)

				/*mempool := wire.NewMsgMemPool()
				wire.WriteMessage(conn, mempool, pver, btcnet)*/
			}

		case *wire.MsgTx:
			{
				log.Printf("TX %s: %d in, %d out", msg.TxSha().String(), len(msg.TxIn), len(msg.TxOut))
				break
			}

		case *wire.MsgPing:
			{
				log.Printf("Ping? Pong!\n")
				pong := wire.NewMsgPong(msg.Nonce)
				wire.WriteMessage(conn, pong, pver, btcnet)
			}

		case *wire.MsgInv:
			{
				log.Printf("Inventory received, requesting %d entries ...\n", len(msg.InvList))

				getdata := wire.NewMsgGetData()
				for _, v := range msg.InvList {
					//log.Printf(" - %s %s\n", v.Type.String(), v.Hash.String())
					if v.Type == wire.InvTypeBlock {
						getdata.AddInvVect(v)
					}
				}

				wire.WriteMessage(conn, getdata, pver, btcnet)
			}

		case *wire.MsgGetData:
			{
				notfound := wire.NewMsgNotFound()
				wire.WriteMessage(conn, notfound, pver, btcnet)
			}

		case *wire.MsgBlock:
			{
				var blockValue uint64

				for _, tx := range msg.Transactions {
					//log.Printf(" - %d in, %d out\n", len(tx.TxIn), len(tx.TxOut))
					for _, txout := range tx.TxOut {
						blockValue += uint64(txout.Value)
					}
				}

				blockValueInNyan := float64(blockValue) / float64(100000000.0)
				log.Printf("Got Block Value: %.8f\n", blockValueInNyan)

				if msg.Header.Timestamp.Before(lastBlock.TS) {
					continue
				}

				nb := nyanBlock{
					Hash:  msg.BlockSha().String(),
					Value: blockValueInNyan,
					TS:    msg.Header.Timestamp,
					NumTx: len(msg.Transactions),
				}
				lastBlock = nb
				{
					localcfg, err := jsoncfg.OpenOrCreate("lastblock.json")
					fatalerr(err)
					localcfg.Set("lastblock", lastBlock)
					err = localcfg.Save()
					fatalerr(err)
				}
				blockBroadcast.Send(nb)
			}

		default:
			{
				log.Printf("Unhandled message type: %s\n", msg.Command())
			}
		}
	}
}

func handleWebSocketClient(ws *websocket.Conn) {
	l := blockBroadcast.Listen()
	defer l.Close()

	log.Printf("WebSocket from %s has connected.\n", ws.RemoteAddr().String())

	// Send lastBlock
	{
		data, err := json.Marshal(lastBlock)
		if err != nil {
			log.Printf("Unable to marshal block to json: %s\n", err.Error())
		} else {
			ws.Write(data)
		}
	}

	// Send live broadcasts
	for v := range l.Ch {
		block := v.(nyanBlock)
		log.Printf("Notifying webclient of block %s ...\n", block.Hash)

		data, err := json.Marshal(block)
		if err != nil {
			log.Printf("Unable to marshal block to json: %s\n", err.Error())
		} else {
			_, err := ws.Write(data)
			if err != nil {
				log.Printf("Unable to write to WebSocket, closing.\n")
				ws.Close()
				break
			}
		}
	}
}

func webSocketListener() {

	http.Handle("/blocks", websocket.Handler(handleWebSocketClient))

	go func() {
		err := http.ListenAndServe(":18337", nil)
		if err != nil {
			log.Fatalf("Could not start WebSocket handler: %s\n", err.Error())
		}
	}()
}

func main() {
	log.Println("Hello Nekonauts!")

	log.Printf("Trying to revert to last saved state ...\n")
	cfg, err := jsoncfg.OpenOrCreate("nyantx.json")
	fatalerr(err)
	cfg.Set("laststart", time.Now().String())
	defer cfg.Save()
	cfg.Save()

	{
		lbcfg, _ := jsoncfg.OpenOrCreate("lastblock.json")
		val, _ := lbcfg.Get("lastblock")

		// dirty hack but it works ¯\_(ツ)_/¯
		buf, _ := json.Marshal(val)
		json.Unmarshal(buf, &lastBlock)

		log.Printf("Resuming from last block on: %s\n", lastBlock.TS.String())
	}

	conn, err := net.Dial("tcp", connectNode)
	fatalerr(err)
	log.Printf("Connection to NyanSeed %s successful!\n", conn.RemoteAddr().String())

	verMsg, err := wire.NewMsgVersionFromConn(conn, uint64(rand.Int63()), 0)
	fatalerr(err)
	fatalerr(verMsg.AddUserAgent("NyanTX", "0.1", "by /u/vmp32k"))
	verMsg.ProtocolVersion = int32(pver)
	verMsg.AddService(wire.ServiceFlag(services))

	log.Printf("Sending version message ...\n")
	fatalerr(wire.WriteMessage(conn, verMsg, pver, btcnet))

	msgs := make(chan wire.Message)

	go func() {
		msg, _, err := wire.ReadMessage(conn, pver, btcnet)
		if err != nil {
			log.Printf("Error reading btcnet message: %s", err.Error())
		}

		msgs <- msg
	}()

	timeout := time.After(5000 * time.Millisecond)

	select {
	case <-timeout:
		log.Fatalln("Bootstrap timeout expired.")
		break
	case msg := <-msgs:
		{
			log.Printf("Got Message: %s", msg.Command())
			switch msg := msg.(type) {
			case *wire.MsgVerAck:
				break
			case *wire.MsgVersion:
				wire.WriteMessage(conn, wire.NewMsgVerAck(), pver, btcnet)
			default:
				log.Printf("Got unexpected response: %s\n", msg.Command())
			}
			break
		}
	}

	blockBroadcast = broadcast.New(10)
	defer blockBroadcast.Close()

	go listenForTx(conn)
	go webSocketListener()

	for {
		time.Sleep(100 * time.Millisecond) // just keep spinnin'
	}
}
