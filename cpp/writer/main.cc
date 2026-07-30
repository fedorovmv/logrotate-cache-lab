#include <arpa/inet.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <signal.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cerrno>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <iomanip>
#include <iostream>
#include <limits>
#include <map>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

namespace {

using Clock = std::chrono::steady_clock;

volatile sig_atomic_t stop_requested = 0;

void handle_signal(int) { stop_requested = 1; }

struct Config {
  std::string run_id;
  std::string path;
  std::string listen = "127.0.0.1:18080";
  std::string state_path;
  std::size_t record_bytes = 512;
  std::size_t buffer_bytes = 64 * 1024;
  std::uint64_t bytes_per_second = 8 * 1024 * 1024;
  std::chrono::milliseconds flush_interval{100};
  std::size_t resident_bytes = 32 * 1024 * 1024;
};

struct State {
  std::atomic<std::uint64_t> generated{0};
  std::atomic<std::uint64_t> written{0};
  std::atomic<std::uint64_t> reopen_requests{0};
  std::atomic<std::uint64_t> reopen_failures{0};
  std::mutex error_mutex;
  std::string last_error;
};

std::string json_escape(const std::string& input) {
  std::ostringstream output;
  for (unsigned char c : input) {
    switch (c) {
      case '"': output << "\\\""; break;
      case '\\': output << "\\\\"; break;
      case '\n': output << "\\n"; break;
      case '\r': output << "\\r"; break;
      case '\t': output << "\\t"; break;
      default:
        if (c < 0x20) {
          output << "\\u" << std::hex << std::setw(4) << std::setfill('0') << static_cast<int>(c);
        } else {
          output << c;
        }
    }
  }
  return output.str();
}

std::string state_json(State& state) {
  std::string last_error;
  {
    std::lock_guard<std::mutex> lock(state.error_mutex);
    last_error = state.last_error;
  }
  std::ostringstream output;
  output << "{\"generated\":" << state.generated.load()
         << ",\"written\":" << state.written.load()
         << ",\"reopen_requests\":" << state.reopen_requests.load()
         << ",\"reopen_failures\":" << state.reopen_failures.load();
  if (!last_error.empty()) {
    output << ",\"last_error\":\"" << json_escape(last_error) << "\"";
  }
  output << "}\n";
  return output.str();
}

bool write_all(int fd, const char* data, std::size_t size, std::string& error) {
  std::size_t offset = 0;
  while (offset < size) {
    const ssize_t count = ::write(fd, data + offset, size - offset);
    if (count > 0) {
      offset += static_cast<std::size_t>(count);
      continue;
    }
    if (count < 0 && errno == EINTR) {
      continue;
    }
    error = std::string("write: ") + std::strerror(errno);
    return false;
  }
  return true;
}

bool write_state_atomic(const std::string& path, State& state, std::string& error) {
  if (path.empty()) return true;
  const std::string temporary = path + ".tmp." + std::to_string(::getpid());
  const int fd = ::open(temporary.c_str(), O_CREAT | O_TRUNC | O_WRONLY, 0600);
  if (fd < 0) {
    error = std::string("open state: ") + std::strerror(errno);
    return false;
  }
  const std::string data = state_json(state);
  bool ok = write_all(fd, data.data(), data.size(), error);
  if (ok && ::fsync(fd) != 0) {
    error = std::string("sync state: ") + std::strerror(errno);
    ok = false;
  }
  if (::close(fd) != 0 && ok) {
    error = std::string("close state: ") + std::strerror(errno);
    ok = false;
  }
  if (ok && ::rename(temporary.c_str(), path.c_str()) != 0) {
    error = std::string("rename state: ") + std::strerror(errno);
    ok = false;
  }
  if (!ok) ::unlink(temporary.c_str());
  return ok;
}

std::chrono::milliseconds parse_duration(const std::string& value) {
  std::size_t suffix = value.size();
  while (suffix > 0 && (value[suffix - 1] < '0' || value[suffix - 1] > '9')) --suffix;
  const std::uint64_t number = std::stoull(value.substr(0, suffix));
  const std::string unit = value.substr(suffix);
  if (unit == "ms") return std::chrono::milliseconds(number);
  if (unit == "s") return std::chrono::milliseconds(number * 1000);
  if (unit == "us") return std::chrono::milliseconds(std::max<std::uint64_t>(1, number / 1000));
  if (unit.empty()) return std::chrono::milliseconds(number);
  throw std::runtime_error("unsupported duration: " + value);
}

Config parse_args(int argc, char** argv) {
  std::map<std::string, std::string> values;
  for (int index = 1; index < argc; ++index) {
    std::string arg(argv[index]);
    if (arg.rfind("--", 0) != 0) throw std::runtime_error("unexpected argument: " + arg);
    const std::size_t equal = arg.find('=');
    if (equal != std::string::npos) {
      values[arg.substr(2, equal - 2)] = arg.substr(equal + 1);
      continue;
    }
    if (index + 1 >= argc) throw std::runtime_error("missing value for " + arg);
    values[arg.substr(2)] = argv[++index];
  }
  Config config;
  if (values.count("run-id")) config.run_id = values["run-id"];
  if (values.count("path")) config.path = values["path"];
  if (values.count("listen")) config.listen = values["listen"];
  if (values.count("state")) config.state_path = values["state"];
  if (values.count("record-bytes")) config.record_bytes = std::stoull(values["record-bytes"]);
  if (values.count("buffer-bytes")) config.buffer_bytes = std::stoull(values["buffer-bytes"]);
  if (values.count("bytes-per-second")) config.bytes_per_second = std::stoull(values["bytes-per-second"]);
  if (values.count("flush-interval")) config.flush_interval = parse_duration(values["flush-interval"]);
  if (values.count("resident-bytes")) config.resident_bytes = std::stoull(values["resident-bytes"]);
  if (config.run_id.empty() || config.path.empty() || config.record_bytes == 0 || config.buffer_bytes == 0 || config.bytes_per_second == 0) {
    throw std::runtime_error("invalid writer configuration");
  }
  const std::size_t prefix_size = config.run_id.size() + 1 + 20 + 1 + 1;
  if (config.record_bytes < prefix_size) throw std::runtime_error("record size is too small");
  return config;
}

int open_log(const std::string& path) {
  return ::open(path.c_str(), O_CREAT | O_APPEND | O_WRONLY, 0640);
}

class HttpServer {
 public:
  HttpServer(const std::string& address, State& state, std::atomic<bool>& reopen)
      : state_(state), reopen_(reopen) {
    const std::size_t colon = address.rfind(':');
    if (colon == std::string::npos) throw std::runtime_error("invalid listen address");
    const std::string host = address.substr(0, colon);
    const int port = std::stoi(address.substr(colon + 1));
    fd_ = ::socket(AF_INET, SOCK_STREAM, 0);
    if (fd_ < 0) throw std::runtime_error(std::string("socket: ") + std::strerror(errno));
    int enabled = 1;
    ::setsockopt(fd_, SOL_SOCKET, SO_REUSEADDR, &enabled, sizeof(enabled));
    sockaddr_in socket_address{};
    socket_address.sin_family = AF_INET;
    socket_address.sin_port = htons(static_cast<std::uint16_t>(port));
    if (::inet_pton(AF_INET, host.c_str(), &socket_address.sin_addr) != 1) {
      ::close(fd_);
      throw std::runtime_error("listen host must be an IPv4 address");
    }
    if (::bind(fd_, reinterpret_cast<sockaddr*>(&socket_address), sizeof(socket_address)) != 0 || ::listen(fd_, 16) != 0) {
      const std::string error = std::strerror(errno);
      ::close(fd_);
      throw std::runtime_error("listen: " + error);
    }
    const int server_fd = fd_;
    thread_ = std::thread([this, server_fd] { serve(server_fd); });
  }

  ~HttpServer() { stop(); }

  void stop() {
    if (fd_ < 0) return;
    ::shutdown(fd_, SHUT_RDWR);
    ::close(fd_);
    fd_ = -1;
    if (thread_.joinable()) thread_.join();
  }

 private:
  void respond(int client, int status, const std::string& body, const std::string& content_type = "text/plain") {
    const char* reason = status == 200 ? "OK" : (status == 405 ? "Method Not Allowed" : "Service Unavailable");
    std::ostringstream response;
    response << "HTTP/1.1 " << status << ' ' << reason << "\r\nContent-Type: " << content_type
             << "\r\nContent-Length: " << body.size() << "\r\nConnection: close\r\n\r\n" << body;
    const std::string bytes = response.str();
    std::size_t offset = 0;
    while (offset < bytes.size()) {
      const ssize_t count = ::send(client, bytes.data() + offset, bytes.size() - offset, 0);
      if (count <= 0) break;
      offset += static_cast<std::size_t>(count);
    }
  }

  void serve(int server_fd) {
    while (!stop_requested) {
      const int client = ::accept(server_fd, nullptr, nullptr);
      if (client < 0) {
        if (errno == EINTR) continue;
        break;
      }
      char request_buffer[4096];
      const ssize_t size = ::recv(client, request_buffer, sizeof(request_buffer) - 1, 0);
      std::string request;
      if (size > 0) request.assign(request_buffer, static_cast<std::size_t>(size));
      if (request.rfind("GET /healthz ", 0) == 0) {
        respond(client, 200, "ok\n");
      } else if (request.rfind("GET /state ", 0) == 0) {
        respond(client, 200, state_json(state_), "application/json");
      } else if (request.rfind("POST /reopen-logs ", 0) == 0) {
        bool expected = false;
        if (reopen_.compare_exchange_strong(expected, true)) {
          respond(client, 200, "queued\n");
        } else {
          respond(client, 503, "reopen already queued\n");
        }
      } else if (request.rfind("GET /reopen-logs ", 0) == 0) {
        respond(client, 405, "POST required\n");
      } else {
        respond(client, 503, "unknown endpoint\n");
      }
      ::close(client);
    }
  }

  int fd_ = -1;
  State& state_;
  std::atomic<bool>& reopen_;
  std::thread thread_;
};

int run(const Config& config) {
  State state;
  std::atomic<bool> reopen_requested{false};
  int log_fd = open_log(config.path);
  if (log_fd < 0) throw std::runtime_error(std::string("open log: ") + std::strerror(errno));

  std::vector<unsigned char> resident(config.resident_bytes);
  const long page_size = std::max<long>(1, ::sysconf(_SC_PAGESIZE));
  for (std::size_t offset = 0; offset < resident.size(); offset += static_cast<std::size_t>(page_size)) resident[offset] = 1;

  std::vector<char> record(config.record_bytes, 'x');
  record.back() = '\n';
  std::copy(config.run_id.begin(), config.run_id.end(), record.begin());
  record[config.run_id.size()] = ' ';
  const std::size_t sequence_offset = config.run_id.size() + 1;
  record[sequence_offset + 20] = ' ';
  std::vector<char> buffer;
  const std::size_t tick_bytes = static_cast<std::size_t>(config.bytes_per_second / 50 + config.record_bytes);
  buffer.reserve(config.buffer_bytes + tick_bytes);

  HttpServer server(config.listen, state, reopen_requested);
  auto last_generate = Clock::now();
  auto last_flush = last_generate;
  long double byte_credit = 0;
  std::string error;
  bool ok = true;

  auto flush = [&]() -> bool {
    if (buffer.empty()) return true;
    if (!write_all(log_fd, buffer.data(), buffer.size(), error)) return false;
    state.written.fetch_add(buffer.size() / config.record_bytes);
    buffer.clear();
    last_flush = Clock::now();
    return true;
  };

  while (!stop_requested) {
    std::this_thread::sleep_for(std::chrono::milliseconds(10));
    const auto now = Clock::now();
    const auto elapsed_ns = std::chrono::duration_cast<std::chrono::nanoseconds>(now - last_generate).count();
    last_generate = now;
    byte_credit += static_cast<long double>(config.bytes_per_second) * static_cast<long double>(elapsed_ns) / 1000000000.0L;
    while (byte_credit >= static_cast<long double>(config.record_bytes)) {
      const std::uint64_t sequence = state.generated.fetch_add(1) + 1;
      char sequence_text[21];
      std::snprintf(sequence_text, sizeof(sequence_text), "%020llu", static_cast<unsigned long long>(sequence));
      std::copy(sequence_text, sequence_text + 20, record.begin() + static_cast<std::ptrdiff_t>(sequence_offset));
      buffer.insert(buffer.end(), record.begin(), record.end());
      byte_credit -= static_cast<long double>(config.record_bytes);
    }

    if (reopen_requested.exchange(false)) {
      state.reopen_requests.fetch_add(1);
      if (::close(log_fd) != 0) {
        error = std::string("close before reopen: ") + std::strerror(errno);
        ok = false;
        break;
      }
      log_fd = open_log(config.path);
      if (log_fd < 0) {
        state.reopen_failures.fetch_add(1);
        error = std::string("reopen log: ") + std::strerror(errno);
        ok = false;
        break;
      }
      if (!flush()) {
        ok = false;
        break;
      }
    } else if (buffer.size() >= config.buffer_bytes || now - last_flush >= config.flush_interval) {
      if (!flush()) {
        ok = false;
        break;
      }
    }
  }

  if (ok && !flush()) ok = false;
  if (log_fd >= 0 && ::close(log_fd) != 0 && ok) {
    error = std::string("close log: ") + std::strerror(errno);
    ok = false;
  }
  if (!ok) {
    std::lock_guard<std::mutex> lock(state.error_mutex);
    state.last_error = error;
  }
  server.stop();
  std::string state_error;
  if (!write_state_atomic(config.state_path, state, state_error)) {
    std::cerr << state_error << '\n';
    return 1;
  }
  if (!ok) std::cerr << error << '\n';
  return ok ? 0 : 1;
}

}  // namespace

int main(int argc, char** argv) {
  ::signal(SIGTERM, handle_signal);
  ::signal(SIGINT, handle_signal);
  ::signal(SIGPIPE, SIG_IGN);
  try {
    return run(parse_args(argc, argv));
  } catch (const std::exception& error) {
    std::cerr << error.what() << '\n';
    return 1;
  }
}
