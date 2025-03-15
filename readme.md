# Pomodoro Timer

A simple Pomodoro timer application with a system tray interface.

## Features
- Start and stop Pomodoro sessions
- Short and long break functionality
- Configurable timer durations
- System tray integration

## Prerequisites
Make sure you have Go installed on your system. You can download it from [golang.org](https://golang.org/).

## Installation
Clone the repository:
```sh
git clone <repository_url>
cd <repository_name>
```

## Building and Running

### macOS & Linux
```sh
go build -o pomodoro-timer ./cmd/pomodoro-timer
./pomodoro-timer
```

### Windows
```sh
go build -ldflags "-s -w -H windowsgui" -o pomodoro-timer.exe ./cmd/pomodoro-timer
pomodoro-timer.exe
```

## Configuration
The application stores its settings in a JSON file located at:
- macOS/Linux: `~/.pomodoro_settings.json`
- Windows: `C:\Users\<YourUsername>\.pomodoro_settings.json`

You can modify the timer settings directly in this file or open it through the application menu.

## Dependencies
This project uses the following Go packages:
- `github.com/Kodeworks/golang-image-ico`
- `github.com/getlantern/systray`
- `github.com/hajimehoshi/oto`
- `golang.org/x/image/font`
- `golang.org/x/image/math/fixed`

Make sure to install them before building:
```sh
go mod tidy
```

## License
This project is licensed under the MIT License.