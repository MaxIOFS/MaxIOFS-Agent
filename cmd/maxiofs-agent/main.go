package main

import (
	"context"
	_ "embed"
	"fmt"
	"sync"

	"maxiofs-agent/internal/cgofuse"
	"maxiofs-agent/internal/config"
	"maxiofs-agent/internal/storage"
	"maxiofs-agent/internal/vfs"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"fyne.io/systray"
	"github.com/gen2brain/dlgs"
)

//go:embed icon.ico
var iconData []byte

//go:embed icon.png
var iconPNG []byte

type App struct {
	config         *config.Config
	s3Client       *storage.S3Client
	mountedBuckets map[string]*MountedBucket
	mu             sync.Mutex

	// Fyne app for windows
	fyneApp fyne.App

	// Menu items
	statusItem     *systray.MenuItem
	connectItem    *systray.MenuItem
	disconnectItem *systray.MenuItem
	bucketsMenu    *systray.MenuItem
	bucketItems    []*systray.MenuItem // Para trackear los items de buckets
}

type MountedBucket struct {
	BucketName  string
	DriveLetter string
	Host        *cgofuse.FileSystemHost
}

var app *App

func main() {
	// Create Fyne app first
	fyneApp := fyneapp.NewWithID("com.maxiofs.agent")

	// Configurar icono de la aplicación (usar PNG para Fyne)
	fyneApp.SetIcon(fyne.NewStaticResource("icon.png", iconPNG))

	app = &App{
		mountedBuckets: make(map[string]*MountedBucket),
		fyneApp:        fyneApp,
	}

	// Cargar configuración
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{UseSSL: true}
	}
	app.config = cfg

	// Crear una ventana invisible para mantener la app viva
	// Esto evita que Fyne cierre la app cuando todas las ventanas visibles se cierran
	dummyWindow := fyneApp.NewWindow("")
	dummyWindow.Resize(fyne.NewSize(1, 1))
	dummyWindow.Hide()

	// Iniciar systray en una goroutine
	go systray.Run(onReady, onExit)

	// Iniciar el bucle de eventos de Fyne (esto debe ser en el thread principal)
	fyneApp.Run()
}

func onReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("MaxIOFS")
	systray.SetTooltip("MaxIOFS Agent - S3 Bucket Mounting")

	// Status
	app.statusItem = systray.AddMenuItem("⚫ Disconnected", "Status")
	app.statusItem.Disable()

	systray.AddSeparator()

	// Connect
	app.connectItem = systray.AddMenuItem("🔌 Configure Connection", "Configure MaxIOFS credentials")

	// Disconnect
	app.disconnectItem = systray.AddMenuItem("🔴 Disconnect", "Disconnect from MaxIOFS")
	app.disconnectItem.Disable()
	app.disconnectItem.Hide()

	systray.AddSeparator()

	// Buckets
	app.bucketsMenu = systray.AddMenuItem("📦 Buckets", "View and mount buckets")
	app.bucketsMenu.Disable()

	systray.AddSeparator()

	// Help
	helpItem := systray.AddMenuItem("❓ Help", "How to use")

	// About
	aboutItem := systray.AddMenuItem("ℹ️ About", "About MaxIOFS Agent")

	systray.AddSeparator()

	// Quit
	quitItem := systray.AddMenuItem("❌ Quit", "Close MaxIOFS Agent")

	// Auto-conectar
	if app.config.Endpoint != "" && app.config.AccessKeyID != "" && app.config.SecretAccessKey != "" {
		tryConnect()
	}

	// Event loop
	go func() {
		for {
			select {
			case <-app.connectItem.ClickedCh:
				go showSettings()
			case <-app.disconnectItem.ClickedCh:
				go disconnect()
			case <-helpItem.ClickedCh:
				go showHelp()
			case <-aboutItem.ClickedCh:
				go showAbout()
			case <-quitItem.ClickedCh:
				go confirmQuit()
			}
		}
	}()
}

func onExit() {
	disconnect()
}

func showSettings() {
	// Usar Do (no DoAndWait) para no bloquear
	fyne.Do(func() {
		// Usar la app Fyne existente, NO crear una nueva
		window := app.fyneApp.NewWindow("MaxIOFS - Connection Settings")
		window.SetIcon(fyne.NewStaticResource("icon.png", iconPNG))
		window.SetFixedSize(true)

		// Create form fields
		endpointEntry := widget.NewEntry()
		endpointEntry.SetPlaceHolder("e.g., localhost:9000 or s3.example.com")
		endpointEntry.SetText(app.config.Endpoint)

		accessKeyEntry := widget.NewEntry()
		accessKeyEntry.SetPlaceHolder("Your Access Key ID")
		accessKeyEntry.SetText(app.config.AccessKeyID)

		secretKeyEntry := widget.NewPasswordEntry()
		secretKeyEntry.SetPlaceHolder("Your Secret Access Key")
		secretKeyEntry.SetText(app.config.SecretAccessKey)

		sslCheck := widget.NewCheck("Use SSL/TLS (Secure Connection)", nil)
		sslCheck.SetChecked(app.config.UseSSL)

		insecureCheck := widget.NewCheck("Skip SSL Certificate Verification (Insecure)", nil)
		insecureCheck.SetChecked(app.config.InsecureSkipVerify)

		// Create form
		form := container.NewVBox(
			widget.NewLabel("Endpoint:"),
			endpointEntry,
			widget.NewLabel("Access Key ID:"),
			accessKeyEntry,
			widget.NewLabel("Secret Access Key:"),
			secretKeyEntry,
			widget.NewLabel(""),
			sslCheck,
			insecureCheck,
		)

		// Create buttons
		saveBtn := widget.NewButton("Connect", func() {
			endpoint := endpointEntry.Text
			accessKey := accessKeyEntry.Text
			secretKey := secretKeyEntry.Text
			useSSL := sslCheck.Checked
			insecureSkipVerify := insecureCheck.Checked

			if endpoint == "" || accessKey == "" || secretKey == "" {
				dialog.ShowError(fmt.Errorf("All fields are required"), window)
				return
			}

			app.config.Endpoint = endpoint
			app.config.AccessKeyID = accessKey
			app.config.SecretAccessKey = secretKey
			app.config.UseSSL = useSSL
			app.config.InsecureSkipVerify = insecureSkipVerify
			app.config.Save()

			window.Close()
			go tryConnect()
		})

		cancelBtn := widget.NewButton("Cancel", func() {
			window.Close()
		})

		buttons := container.NewGridWithColumns(2, cancelBtn, saveBtn)

		// Layout
		content := container.NewVBox(
			widget.NewLabelWithStyle("Connection Settings", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
			form,
			widget.NewSeparator(),
			buttons,
		)

		window.SetContent(container.NewPadded(content))
		window.CenterOnScreen()
		window.Show() // Usar Show() en lugar de ShowAndRun()
	})
}

func tryConnect() {
	go func() {
		app.statusItem.SetTitle("🟡 Connecting...")

		client, err := storage.NewS3Client(
			app.config.Endpoint,
			app.config.AccessKeyID,
			app.config.SecretAccessKey,
			app.config.UseSSL,
			app.config.InsecureSkipVerify,
		)
		if err != nil {
			app.statusItem.SetTitle("⚫ Connection error")
			dlgs.Error("Error", "Could not connect: "+err.Error())
			return
		}

		ctx := context.Background()
		if err := client.TestConnection(ctx); err != nil {
			app.statusItem.SetTitle("⚫ Connection error")
			dlgs.Error("Error", "Could not connect: "+err.Error())
			return
		}

		app.mu.Lock()
		app.s3Client = client
		app.mu.Unlock()

		app.statusItem.SetTitle("🟢 Connected - " + app.config.Endpoint)
		app.connectItem.Disable()
		app.disconnectItem.Enable()
		app.disconnectItem.Show()

		loadBuckets()
		dlgs.Info("Connection Successful", "Connected to MaxIOFS")
	}()
}

func disconnect() {
	app.mu.Lock()
	defer app.mu.Unlock()

	// Unmount all buckets
	for _, mounted := range app.mountedBuckets {
		if mounted.Host != nil {
			mounted.Host.Unmount()
		}
	}
	app.mountedBuckets = make(map[string]*MountedBucket)

	// Hide and clear bucket items
	for _, item := range app.bucketItems {
		item.Hide()
	}
	app.bucketItems = nil

	app.s3Client = nil
	app.statusItem.SetTitle("⚫ Disconnected")
	app.connectItem.Enable()
	app.disconnectItem.Disable()
	app.disconnectItem.Hide()
	app.bucketsMenu.Disable()
}

func loadBuckets() {
	if app.s3Client == nil {
		return
	}

	// Clear previous items
	for _, item := range app.bucketItems {
		item.Hide()
	}
	app.bucketItems = nil

	ctx := context.Background()
	buckets, err := app.s3Client.ListBuckets(ctx)
	if err != nil {
		dlgs.Error("Error", "Error listing buckets: "+err.Error())
		return
	}

	app.bucketsMenu.Enable()

	for _, bucket := range buckets {
		bucketName := bucket.Name
		item := app.bucketsMenu.AddSubMenuItem("📦 "+bucketName, "Click to mount as drive")
		app.bucketItems = append(app.bucketItems, item) // Track the item

		go func(name string, menuItem *systray.MenuItem) {
			for {
				<-menuItem.ClickedCh
				toggleBucketMount(name, menuItem)
			}
		}(bucketName, item)
	}
}

func toggleBucketMount(bucketName string, menuItem *systray.MenuItem) {
	app.mu.Lock()

	// If already mounted, unmount
	if mounted, exists := app.mountedBuckets[bucketName]; exists {
		if mounted.Host != nil {
			mounted.Host.Unmount()
		}
		delete(app.mountedBuckets, bucketName)
		app.mu.Unlock()

		menuItem.SetTitle("📦 " + bucketName)
		dlgs.Info("Unmounted", "Bucket unmounted successfully")
		return
	}
	app.mu.Unlock()

	// Request drive letter
	driveLetter, ok, _ := dlgs.Entry(
		"Mount Bucket",
		"Drive letter (e.g., Z):",
		"Z",
	)
	if !ok || driveLetter == "" {
		return
	}
	driveLetter = driveLetter[:1] // Only first letter
	mountPoint := driveLetter + ":"

	// Create filesystem
	fs := vfs.NewS3FS(app.s3Client, bucketName)
	host := cgofuse.NewFileSystemHost(fs)

	// Enable write capabilities
	host.SetCapCaseInsensitive(false)
	host.SetCapReaddirPlus(false)

	// Simplified mount options
	mountOpts := []string{
		"-o", "volname=" + bucketName,
		"-o", "umask=0",
	}

	fmt.Printf("Mounting bucket '%s' on '%s' with write permissions...\n", bucketName, mountPoint)

	// Mount in goroutine
	go func() {
		if !host.Mount(mountPoint, mountOpts) {
			dlgs.Error("Error", fmt.Sprintf("Could not mount bucket '%s' on '%s'", bucketName, mountPoint))
			return
		}
		fmt.Printf("Mount completed for %s\n", bucketName)
	}()

	// Save reference
	app.mu.Lock()
	app.mountedBuckets[bucketName] = &MountedBucket{
		BucketName:  bucketName,
		DriveLetter: driveLetter,
		Host:        host,
	}
	app.mu.Unlock()

	menuItem.SetTitle("✅ " + bucketName + " (" + driveLetter + ":)")
	dlgs.Info("Mounted", fmt.Sprintf("Bucket '%s' mounted on %s:\n\nAccess from Windows Explorer", bucketName, driveLetter+":"))
}

func showHelp() {
	dlgs.Info("Help - MaxIOFS Agent",
		"How to use:\n\n"+
			"1. Configure Connection → Enter credentials\n"+
			"2. Buckets → Click on a bucket\n"+
			"3. Choose a drive letter (e.g., Z)\n"+
			"4. Done! Access from Windows Explorer\n\n"+
			"Files are loaded on demand.\n"+
			"Does not download the entire bucket.")
}

func showAbout() {
	dlgs.Info("About - MaxIOFS Agent",
		"MaxIOFS Agent\n"+
			"Version: 0.1.0-alpha\n\n"+
			"S3-compatible bucket mounting tool for Windows.\n"+
			"Mount your S3 buckets as local drives.\n\n"+
			"GitHub: https://github.com/MaxIOFS/MaxIOFS-Agent")
}

func confirmQuit() {
	ok, _ := dlgs.Question("Quit", "Are you sure you want to quit MaxIOFS Agent?", false)
	if ok {
		disconnect()
		systray.Quit()
		app.fyneApp.Quit()
	}
}
