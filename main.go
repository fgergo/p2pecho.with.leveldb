// This is a demonstration of using leveldb with go-libp2p
//
// $ go install github.com/fgergo/p2pecho.with.leveldb@latest
//
// in the first terminal window:
//
// $ mkdir -p /tmp/d1 && cd /tmp/d1 && p2pecho.with.leveldb -l 10000
//
// in the second terminal window, follow instructions in displayed in first terminal window, similar to this:
//
// $ mkdir -p /tmp/d2 && cd /tmp/d2 && p2pecho.with.leveldb -l 10001 -d /ip4/127.0.0.1/tcp/10000/p2p/QmUD2KWcEyjVkFv4LnRRmJ8aURJC6X35oqUmyzYrtn3ERr
//
// to dump db with timestamps:
//
// $ cd /tmp/d1 && p2pecho.with.leveldb -D
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"os"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/syndtr/goleveldb/leveldb"

	golog "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
)

const dbPath = "./echo_db"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	golog.SetAllLoggers(golog.LevelInfo)

	listenF := flag.Int("l", 0, "wait for incoming connections")
	targetF := flag.String("d", "", "target peer to dial")
	insecureF := flag.Bool("insecure", false, "use an unencrypted connection")
	seedF := flag.Int64("seed", 0, "set random seed for id generation")
	dumpF := flag.Bool("D", false, "dump leveldb database")

	flag.Parse()

	if *dumpF {
		dumpDb(dbPath)
		os.Exit(0)
	}

	if *listenF == 0 {
		log.Fatal("Please provide a port to bind on with -l")
	}

	ha, err := makeBasicHost(*listenF, *insecureF, *seedF)
	if err != nil {
		log.Fatal(err)
	}

	if *targetF == "" {
		startListener(ctx, ha, *listenF, *insecureF)
		<-ctx.Done()
	} else {
		runSender(ctx, ha, *targetF)
	}
}

func makeBasicHost(listenPort int, insecure bool, randseed int64) (host.Host, error) {
	var r io.Reader
	if randseed == 0 {
		r = rand.Reader
	} else {
		r = mrand.New(mrand.NewSource(randseed))
	}

	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
	if err != nil {
		return nil, err
	}

	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", listenPort)),
		libp2p.Identity(priv),
		libp2p.DisableRelay(),
	}

	if insecure {
		opts = append(opts, libp2p.NoSecurity)
	}

	return libp2p.New(opts...)
}

func getHostAddress(ha host.Host) string {
	hostAddr, _ := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s", ha.ID()))
	addr := ha.Addrs()[0]
	return addr.Encapsulate(hostAddr).String()
}

func startListener(ctx context.Context, ha host.Host, listenPort int, insecure bool) {
	fullAddr := getHostAddress(ha)
	log.Printf("I am %s\n", fullAddr)

	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ha.SetStreamHandler("/echo/1.0.0", func(s network.Stream) {
		log.Println("listener received new stream")
		if err := doEcho(s, db); err != nil {
			log.Println(err)
			s.Reset()
		} else {
			s.Close()
		}
	})

	log.Println("listening for connections")

	if insecure {
		log.Printf("Now run \"%v -l %d -d %s -insecure\" on a different terminal\n", os.Args[0], listenPort+1, fullAddr)
	} else {
		log.Printf("Now run \"%v -l %d -d %s\" on a different terminal\n", os.Args[0], listenPort+1, fullAddr)
	}

	<-ctx.Done()
}

func runSender(ctx context.Context, ha host.Host, targetPeer string) {
	fullAddr := getHostAddress(ha)
	log.Printf("I am %s\n", fullAddr)

	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	maddr, err := ma.NewMultiaddr(targetPeer)
	if err != nil {
		log.Println(err)
		return
	}

	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		log.Println(err)
		return
	}

	ha.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)

	log.Println("sender opening stream")
	s, err := ha.NewStream(context.Background(), info.ID, "/echo/1.0.0")
	if err != nil {
		log.Println(err)
		return
	}

	log.Println("runSender, sender saying hello")
	now := time.Now()
	_, err = s.Write([]byte(fmt.Sprintf("Hello sending at: %v \n", now)))
	if err != nil {
		log.Println(err)
		return
	}

	out, err := io.ReadAll(s)
	if err != nil {
		log.Println(err)
		return
	}

	log.Printf("runSender, read reply : %q, duration: %v\n", out, time.Since(now))
}

func doEcho(s network.Stream, db *leveldb.DB) error {
	buf := bufio.NewReader(s)
	str, err := buf.ReadString('\n')
	if err != nil {
		return err
	}

	log.Printf("doEcho, read: %s", str)

	timestamp := time.Now().UnixNano()
	if err := storeTimestamp(db, timestamp, "received"); err != nil {
		return err
	}

	// Sleep to simulate some processing time before reading the reply
	time.Sleep(1 * time.Second)

	replyTimestamp := time.Now().UnixNano()
	if err := storeTimestamp(db, replyTimestamp, "replied"); err != nil {
		return err
	}

	str = fmt.Sprintf("doEcho, reply at: %v", time.Now())
	_, err = s.Write([]byte(str))
	if err != nil {
		return err
	}

	duration := time.Duration(replyTimestamp - timestamp)
	log.Printf("Duration between sent and received: %v", duration)

	return nil
}

func storeTimestamp(db *leveldb.DB, timestamp int64, messageType string) error {
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, uint64(timestamp))

	key := []byte(messageType)
	err := db.Put(key, value, nil)
	return err
}

func dumpDb(dbPath string) {
	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Create an iterator to iterate over all key-value pairs
	iter := db.NewIterator(nil, nil)
	defer iter.Release()

	fmt.Println("Last event pair:")
	// Iterate over the key-value pairs
	for iter.Next() {
		key := iter.Key()
		value := iter.Value()

		fmt.Printf("Event: %s, timestamp: %v\n", key, time.Unix(0, int64(binary.BigEndian.Uint64(value))))
	}

	if err := iter.Error(); err != nil {
		log.Fatal(err)
	}

}
