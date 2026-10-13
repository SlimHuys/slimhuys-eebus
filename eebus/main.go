// SlimHuys EEBUS connects an EEBUS device (tested: Vaillant VR940 heat pump
// gateway) to Home Assistant through MQTT discovery.
package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/service"
	"github.com/enbility/eebus-go/usecases/ma/mpc"
	"github.com/enbility/ship-go/cert"
	"github.com/enbility/spine-go/model"
	"github.com/enbility/spine-go/spine"
)

var version = "dev"

// loadOrCreateCert keeps the certificate in the data dir: the SKI derived
// from it is what the user trusts in the manufacturer app, so it must
// survive restarts and updates.
func loadOrCreateCert(dir string) (tls.Certificate, error) {
	crtPath, keyPath := filepath.Join(dir, "eebus.crt"), filepath.Join(dir, "eebus.key")
	if c, err := tls.LoadX509KeyPair(crtPath, keyPath); err == nil {
		return c, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	c, err := cert.CreateCertificate("SlimHuys", "SlimHuys", "NL", "SlimHuys-EEBUS")
	if err != nil {
		return c, err
	}
	key, err := x509.MarshalECPrivateKey(c.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		return c, err
	}
	if err := os.WriteFile(crtPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]}), 0o600); err != nil {
		return c, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		return c, err
	}
	log.Printf("Created a new certificate in %s", dir)
	return c, nil
}

func skiOf(c tls.Certificate) (string, error) {
	leaf := c.Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return "", err
		}
	}
	return cert.SkiFromCertificate(leaf)
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.Printf("SlimHuys EEBUS %s", version)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	certificate, err := loadOrCreateCert(cfg.DataDir)
	if err != nil {
		log.Fatalf("certificate: %v", err)
	}
	localSKI, err := skiOf(certificate)
	if err != nil {
		log.Fatalf("certificate: %v", err)
	}
	serial := localSKI[:8]

	conf, err := api.NewConfiguration(
		"SlimHuys", "SlimHuys", "EEBUS-Bridge", serial,
		model.DeviceTypeTypeEnergyManagementSystem,
		[]model.EntityTypeType{model.EntityTypeTypeCEM},
		cfg.Port, certificate, 4*time.Second)
	if err != nil {
		log.Fatalf("eebus configuration: %v", err)
	}
	conf.SetAlternateIdentifier("SlimHuys-EEBUS-" + serial)

	b := NewBridge(cfg)
	b.svc = service.NewService(conf, b)
	b.svc.SetLogging(newLogger(cfg.Debug))
	if err := b.svc.Setup(); err != nil {
		log.Fatalf("eebus setup: %v", err)
	}
	b.local = b.svc.LocalDevice().EntityForType(model.EntityTypeTypeCEM)
	// MPC adds the Measurement and ElectricalConnection client features to
	// our CEM entity; subscriptions on other entities are done by the poller.
	b.svc.AddUseCase(mpc.NewMPC(b.local, nil))
	if err := spine.Events.Subscribe(b); err != nil {
		log.Fatalf("eebus events: %v", err)
	}

	log.Printf("Local SKI: %s (trust this in the manufacturer app, e.g. myVAILLANT → Network → EEBUS)", localSKI)
	b.svc.Start()
	if cfg.DeviceSKI != "" {
		b.useSKI(cfg.DeviceSKI)
	} else {
		log.Printf("No device_ski configured; waiting for a single EEBUS device to show up on the network")
	}
	go b.loop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Printf("Shutting down")
	if pub := b.publisher(); pub != nil {
		pub.Close()
	}
	b.svc.Shutdown()
}

// logger adapts eebus-go logging to the standard logger.
type logger struct{ debug bool }

func newLogger(debug bool) *logger { return &logger{debug: debug} }

func (l *logger) Trace(...interface{})          {}
func (l *logger) Tracef(string, ...interface{}) {}
func (l *logger) Debug(args ...interface{}) {
	if l.debug {
		log.Print(append([]interface{}{"DEBUG "}, args...)...)
	}
}
func (l *logger) Debugf(format string, args ...interface{}) {
	if l.debug {
		log.Printf("DEBUG "+format, args...)
	}
}
func (l *logger) Info(args ...interface{}) {
	if l.debug {
		log.Print(args...)
	}
}
func (l *logger) Infof(format string, args ...interface{}) {
	if l.debug {
		log.Printf(format, args...)
	}
}
func (l *logger) Error(args ...interface{}) { log.Print(append([]interface{}{"ERROR "}, args...)...) }
func (l *logger) Errorf(format string, args ...interface{}) {
	log.Print("ERROR " + fmt.Sprintf(format, args...))
}
