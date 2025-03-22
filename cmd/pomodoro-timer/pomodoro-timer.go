package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"io/ioutil"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/hajimehoshi/go-mp3"
	"github.com/lutischan-ferenc/systray"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/windows/registry"
	"image/png"
)

var (
	//go:embed assets/numbers.ttf
	numbersTtf []byte

	//go:embed assets/clock.mp3
	clockSoundMP3 []byte

	// MP3 player params
	mp3Decoder    *mp3.Decoder
	clockPlayer   *oto.Player
	clockStopCh   chan struct{}
	clockMutex    sync.Mutex
	clockSoundPCM []byte

	err           error
	context       *oto.Context
	pomodoroCount int           // Tracks the number of completed Pomodoro sessions
	isRunning     bool          // Indicates if the timer is currently running
	isInPomodoro  bool          // Indicates if the current session is a Pomodoro
	remainingTime time.Duration // Tracks the remaining time for the current session
	stopCh        chan struct{} // Channel to stop the timer
	mu            sync.Mutex    // Mutex for thread-safe operations

	ticker         *time.Ticker
	oldDisplayText string
	settings       TimerSettings // Stores Pomodoro timer settings

	mPomodoro  *systray.MenuItem // Menu item for starting a Pomodoro session
	mBreak     *systray.MenuItem // Menu item for starting a break
	mLongBreak *systray.MenuItem // Menu item for starting a long break
	mAutoStart *systray.MenuItem
	baseImage  *image.RGBA // Base image for the system tray icon
	fontFace   font.Face   // Font face for rendering text on the icon
)

// main is the entry point of the application.
func main() {
	initMp3Player()
	initResources()
	initAudio()
	stopCh = make(chan struct{})
	loadSettings()
	systray.Run(onReady, nil)
}

func initMp3Player() {
	reader := bytes.NewReader(clockSoundMP3)
	mp3Decoder, err = mp3.NewDecoder(reader)
	if err != nil {
		fmt.Println("Error init sound player:", err)
		return
	}

	// Decode MP3 to PCM
	var pcmData []byte
	buf := make([]byte, 1024)
	for {
		n, err := mp3Decoder.Read(buf)
		if n > 0 {
			pcmData = append(pcmData, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Println("Error decoding MP3:", err)
			return
		}
	}
	clockSoundPCM = pcmData
}

// TimerSettings stores the durations for Pomodoro, short break, and long break.
type TimerSettings struct {
	PomodoroDuration   int  `json:"pomodoro_duration"`    // Duration of a Pomodoro session in minutes
	ShortBreakDuration int  `json:"short_break_duration"` // Duration of a short break in minutes
	LongBreakDuration  int  `json:"long_break_duration"`  // Duration of a long break in minutes
	EnableClockSound   bool `json:"enable_clock_sound"`
}

// initResources initializes the base image and font for the system tray icon.
func initResources() {
	baseImage = image.NewRGBA(image.Rect(0, 0, 64, 64))
	darkRed := color.RGBA{139, 0, 0, 255} // Dark red background color
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			baseImage.Set(x, y, darkRed)
		}
	}

	// Load and parse the embedded font
	fnt, err := opentype.Parse(numbersTtf)
	if err != nil {
		fmt.Println("Error parsing font:", err)
		return
	}
	fontFace, err = opentype.NewFace(fnt, &opentype.FaceOptions{
		Size:    46,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		fmt.Println("Error creating font face:", err)
		return
	}
}

// initAudio initializes the audio context.
func initAudio() error {
	op := &oto.NewContextOptions{
		SampleRate:   mp3Decoder.SampleRate(),
		ChannelCount: 2,
		Format:       oto.FormatSignedInt16LE,
	}

	var err error
	var ready chan struct{}
	context, ready, err = oto.NewContext(op)
	if err != nil {
		return fmt.Errorf("failed to create audio context: %v", err)
	}

	// Wait for the context to be ready
	<-ready
	return nil
}

// playTickSound plays a short beep sound.
func playTickSound() {
	if context == nil {
		fmt.Println("Audio context not initialized")
		return
	}

	freq := 440.0 // Frequency of the sound in Hz (A4 note)
	duration := 200 * time.Millisecond
	amplitude := 0.3 // Amplitude of the sound

	// Create a sine wave for the sound
	sineWave := NewSineWave(freq, duration, 1, oto.FormatSignedInt16LE, amplitude)

	// Create a new player for the sound
	player := context.NewPlayer(sineWave)
	player.Play()

	// Wait for the sound to finish playing
	time.Sleep(duration)
	player.Close() // Close the player after the sound is done
}

// NewSineWave creates a sine wave for the given frequency, duration, and format.
func NewSineWave(freq float64, duration time.Duration, channelCount int, format oto.Format, amplitude float64) *SineWave {
	sampleRate := 44100 // Sample rate
	length := int64(float64(sampleRate) * float64(duration) / float64(time.Second))
	return &SineWave{
		freq:         freq,
		length:       length,
		channelCount: channelCount,
		format:       format,
		amplitude:    amplitude,
		sampleRate:   sampleRate,
	}
}

// SineWave implements io.Reader to generate a sine wave.
type SineWave struct {
	freq         float64
	length       int64
	pos          int64
	channelCount int
	format       oto.Format
	amplitude    float64
	sampleRate   int
}

// Read generates the sine wave data.
func (s *SineWave) Read(buf []byte) (int, error) {
	if s.pos >= s.length {
		return 0, io.EOF
	}

	eof := false
	if s.pos+int64(len(buf)) > s.length {
		buf = buf[:s.length-s.pos]
		eof = true
	}

	length := float64(s.sampleRate) / s.freq
	num := formatByteLength(s.format) * s.channelCount
	p := s.pos / int64(num)

	switch s.format {
	case oto.FormatSignedInt16LE:
		for i := 0; i < len(buf)/num; i++ {
			const max = 32767
			b := int16(math.Sin(2*math.Pi*float64(p)/length) * s.amplitude * max)
			for ch := 0; ch < s.channelCount; ch++ {
				buf[num*i+2*ch] = byte(b)
				buf[num*i+1+2*ch] = byte(b >> 8)
			}
			p++
		}
	}

	s.pos += int64(len(buf))

	if eof {
		return len(buf), io.EOF
	}
	return len(buf), nil
}

// formatByteLength returns the byte length of the given format.
func formatByteLength(format oto.Format) int {
	switch format {
	case oto.FormatFloat32LE:
		return 4
	case oto.FormatUnsignedInt8:
		return 1
	case oto.FormatSignedInt16LE:
		return 2
	default:
		panic(fmt.Sprintf("unexpected format: %d", format))
	}
}

// getSettingsPath returns the path to the settings file.
func getSettingsPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	return filepath.Join(homeDir, ".pomodoro_settings.json")
}

// loadSettings loads the timer settings from a file or uses defaults.
func loadSettings() {
	settings = TimerSettings{
		PomodoroDuration:   25,
		ShortBreakDuration: 5,
		LongBreakDuration:  15,
		EnableClockSound:   true,
	}

	filePath := getSettingsPath()
	data, err := ioutil.ReadFile(filePath)
	if err == nil {
		err = json.Unmarshal(data, &settings)
		if err != nil {
			fmt.Println("Failed to load settings:", err)
		}
	}
}

// saveSettings saves the current timer settings to a file.
func saveSettings() {
	filePath := getSettingsPath()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Println("Failed to save settings:", err)
		return
	}
	err = ioutil.WriteFile(filePath, data, 0644)
	if err != nil {
		fmt.Println("Failed to write settings file:", err)
	}
}

// openSettingsEditor opens the settings file in the default text editor.
func openSettingsEditor() {
	tempFile, err := ioutil.TempFile("", "pomodoro_settings_*.json")
	if err != nil {
		fmt.Println("Error creating temp file:", err)
		return
	}
	defer os.Remove(tempFile.Name())

	data, _ := json.MarshalIndent(settings, "", "  ")
	tempFile.Write(data)
	tempFile.Close()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("notepad", tempFile.Name())
	case "darwin":
		cmd = exec.Command("open", "-t", tempFile.Name())
	default:
		cmd = exec.Command("xdg-open", tempFile.Name())
	}

	err = cmd.Start()
	if err != nil {
		fmt.Println("Failed to open editor:", err)
		return
	}

	cmd.Wait()

	updatedData, err := ioutil.ReadFile(tempFile.Name())
	if err != nil {
		fmt.Println("Failed to read updated settings:", err)
		return
	}

	var newSettings TimerSettings
	err = json.Unmarshal(updatedData, &newSettings)
	if err != nil {
		fmt.Println("Invalid JSON format:", err)
		return
	}

	settings = newSettings
	saveSettings()
}

// onReady sets up the system tray interface.
func onReady() {
	systray.SetTitle("Pomodoro Timer")
	systray.SetTooltip("Click to start Pomodoro")
	systray.SetIconFromMemory(generateIconWithDots("▶", pomodoroCount))

	// Handle direct tray icon clicks
	systray.SetOnClick(func(menu systray.IMenu) {
		handleTrayClick()
	})

	mWeb := systray.AddMenuItem("Pomodoro Timer v1.4.0", "Open the website in browser")
	mWeb.Click(func() {
		openBrowser("https://github.com/lutischan-ferenc/pomodoro-timer")
	})
	systray.AddSeparator()
	mPomodoro = systray.AddMenuItem("Start Pomodoro", "Start a new Pomodoro session")
	mPomodoro.Click(func() {
		mu.Lock()
		isInPomodoro = true
		mu.Unlock()
		handleTimerClick(time.Duration(settings.PomodoroDuration) * time.Minute)
	})
	mBreak = systray.AddMenuItem("Start Break", "Take a break")
	mBreak.Click(func() {
		mu.Lock()
		isInPomodoro = false
		mu.Unlock()
		handleTimerClick(time.Duration(settings.ShortBreakDuration) * time.Minute)
	})
	mLongBreak = systray.AddMenuItem("Start Long Break", "Take a long break")
	mLongBreak.Click(func() {
		mu.Lock()
		isInPomodoro = false
		mu.Unlock()
		handleTimerClick(time.Duration(settings.LongBreakDuration) * time.Minute)
	})

	addAutoStartMenuOnWin()
	mClockSound := systray.AddMenuItemCheckbox("Clock sound", "Play ticking sound during Pomodoro", settings.EnableClockSound)
	mClockSound.Click(func() {
		settings.EnableClockSound = !settings.EnableClockSound
		if settings.EnableClockSound {
			mClockSound.Check()
		} else {
			mClockSound.Uncheck()
		}
		saveSettings()
	})

	systray.AddSeparator()
	mSettings := systray.AddMenuItem("Settings", "Configure timers")
	mSettings.Click(func() {
		openSettingsEditor()
	})
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Exit", "Exit the application")
	mQuit.Click(func() {
		systray.Quit()
	})
}

// handleTrayClick handles clicks on the system tray icon
func handleTrayClick() {
	mu.Lock()
	defer mu.Unlock()

	if isRunning {
		// Stop the running timer
		close(stopCh)
		stopCh = make(chan struct{})
		isRunning = false
		systray.SetIconFromMemory(generateIconWithDots("▶", pomodoroCount))
		if isInPomodoro {
			systray.SetTooltip("Pomodoro stopped - Click to start Break")
		} else {
			systray.SetTooltip("Break stopped - Click to start Pomodoro")
		}
	} else {
		// Start the next appropriate timer
		if isInPomodoro {
			var breakDuration time.Duration
			if pomodoroCount == 4 {
				breakDuration = time.Duration(settings.LongBreakDuration) * time.Minute
			} else {
				breakDuration = time.Duration(settings.ShortBreakDuration) * time.Minute
			}
			isInPomodoro = false // Set before starting break
			startTimer(breakDuration)
		} else {
			isInPomodoro = true // Set before starting Pomodoro
			startTimer(time.Duration(settings.PomodoroDuration) * time.Minute)
		}
	}
}

// handleTimerClick starts a timer with the specified duration (used by menu items)
func handleTimerClick(duration time.Duration) {
	mu.Lock()
	defer mu.Unlock()

	if isRunning {
		close(stopCh)
		stopCh = make(chan struct{})
		isRunning = false
	}
	startTimer(duration)
}

// startTimer starts the countdown timer.
func startTimer(duration time.Duration) {
	isRunning = true
	remainingTime = duration
	if isInPomodoro && settings.EnableClockSound {
		playClockSound()
	}
	go func() {
		defer stopClockSound()
		ticker = time.NewTicker(time.Second)
		defer ticker.Stop()

		for remainingTime > 0 {
			select {
			case <-ticker.C:
				mu.Lock()
				remainingTime -= time.Second
				if remainingTime <= 0 {
					isRunning = false
					if isInPomodoro {
						pomodoroCount++
						if pomodoroCount > 4 {
							pomodoroCount = 1
						}
						systray.SetTooltip("Finished pomodoro - Click to start break")
					} else {
						systray.SetTooltip("Finished break - Click to start pomodoro")
					}
					systray.SetIconFromMemory(generateIconWithDots("▶", pomodoroCount))
					playTickSound()
					mu.Unlock()
					return
				}
				if remainingTime < 11*time.Second {
					playTickSound()
				}
				var displayText string
				if remainingTime < time.Minute {
					displayText = fmt.Sprintf("%d", int(remainingTime.Seconds()))
				} else {
					displayText = fmt.Sprintf("%d", int(remainingTime.Minutes()))
				}
				if displayText != oldDisplayText {
					systray.SetIconFromMemory(generateIconWithDots(displayText, pomodoroCount))
					oldDisplayText = displayText
				}
				systray.SetTooltip(fmt.Sprintf("%02d:%02d", int(remainingTime.Minutes()), int(remainingTime.Seconds())%60))
				mu.Unlock()
			case <-stopCh:
				ticker.Stop()
				return
			}
		}
	}()
}

type loopReader struct {
	r io.ReadSeeker
}

func (lr *loopReader) Read(p []byte) (int, error) {
	n, err := lr.r.Read(p)
	if err == io.EOF {
		_, seekErr := lr.r.Seek(0, io.SeekStart)
		if seekErr != nil {
			return n, seekErr
		}
		return n, nil
	}
	return n, err
}

func playClockSound() {
	clockMutex.Lock()
	defer clockMutex.Unlock()

	if !settings.EnableClockSound {
		return
	}
	if context == nil {
		return
	}
	if clockPlayer != nil {
		return // vagy megfelelően kezelje
	}

	clockStopCh = make(chan struct{})

	lr := &loopReader{r: bytes.NewReader(clockSoundPCM)}
	clockPlayer = context.NewPlayer(lr)
	clockPlayer.Play()

	go func() {
		<-clockStopCh
		clockPlayer.Close()
		clockMutex.Lock()
		clockPlayer = nil
		clockMutex.Unlock()
	}()
}

func stopClockSound() {
	clockMutex.Lock()
	defer clockMutex.Unlock()

	if clockPlayer != nil {
		close(clockStopCh)
	}
}

// generateIconWithDots generates an icon with the remaining time and Pomodoro count dots.
func generateIconWithDots(text string, dotCount int) []byte {
	img := image.NewRGBA(baseImage.Bounds())
	copy(img.Pix, baseImage.Pix)

	bounds, _ := font.BoundString(fontFace, text)
	textWidth := (bounds.Max.X - bounds.Min.X).Ceil()
	textHeight := (bounds.Max.Y - bounds.Min.Y).Ceil()

	x := (64 - textWidth) / 2
	y := (64+textHeight)/2 - 5

	col := color.White // White text color
	point := fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)}

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: fontFace,
		Dot:  point,
	}
	d.DrawString(text)

	dotColor := color.RGBA{144, 238, 144, 255} // Light green dots
	dotRadius := 6
	dotSpacing := 5
	startX := 5

	for i := 0; i < dotCount; i++ {
		dotX := startX + i*(dotRadius*2+dotSpacing)
		dotY := 56
		drawCircle(img, dotX, dotY, dotRadius, dotColor)
	}

	var pngBuf bytes.Buffer
	err := png.Encode(&pngBuf, img)
	if err != nil {
		return []byte{0x00}
	}

	return pngBuf.Bytes()
}

// drawCircle draws a circle on the image.
func drawCircle(img *image.RGBA, x, y, radius int, col color.RGBA) {
	for i := -radius; i <= radius; i++ {
		for j := -radius; j <= radius; j++ {
			if i*i+j*j <= radius*radius {
				img.Set(x+i, y+j, col)
			}
		}
	}
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) {
	var err error

	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}

	if err != nil {
		fmt.Println("Failed to open browser:", err)
	}
}

const AUTO_START_NAME = "PomodoroTimer"

func addAutoStartMenuOnWin() {
	// Add auto-start menu item for Windows only
	if runtime.GOOS == "windows" {
		systray.AddSeparator()
		mAutoStart = systray.AddMenuItemCheckbox("Start on System Startup", "Auto-start on System Startup", false)
		// Check the current state of auto-start in the registry
		if isAutoStartEnabled() {
			mAutoStart.Check()
		}

		mAutoStart.Click(func() {
			if mAutoStart.Checked() {
				// Disable auto-start
				if err := setAutoStart(false); err != nil {
					fmt.Println("Failed to disable auto-start:", err)
				} else {
					mAutoStart.Uncheck()
				}
			} else {
				// Enable auto-start
				if err := setAutoStart(true); err != nil {
					fmt.Println("Failed to enable auto-start:", err)
				} else {
					mAutoStart.Check()
				}
			}
		})
	}
}

// setAutoStart sets or removes the application from the Windows startup registry.
func setAutoStart(enable bool) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("auto-start is only supported on Windows")
	}

	// Get the path to the current executable
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}

	// Open the registry key for auto-start programs
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("failed to open registry key: %v", err)
	}
	defer key.Close()

	// Set or remove the auto-start entry
	if enable {
		if err := key.SetStringValue(AUTO_START_NAME, exePath); err != nil {
			return fmt.Errorf("failed to set registry value: %v", err)
		}
	} else {
		if err := key.DeleteValue(AUTO_START_NAME); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("failed to delete registry value: %v", err)
		}
	}

	return nil
}

// isAutoStartEnabled checks if the application is set to auto-start in the Windows registry.
func isAutoStartEnabled() bool {
	if runtime.GOOS != "windows" {
		return false
	}

	// Get the path to the current executable
	exePath, err := os.Executable()
	if err != nil {
		fmt.Println("Failed to get executable path:", err)
		return false
	}

	// Open the registry key for auto-start programs
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		fmt.Println("Failed to open registry key:", err)
		return false
	}
	defer key.Close()

	// Check if the registry value exists and matches the current executable path
	value, _, err := key.GetStringValue(AUTO_START_NAME)
	if err != nil {
		if err == registry.ErrNotExist {
			return false
		}
		fmt.Println("Failed to read registry value:", err)
		return false
	}

	return value == exePath
}
