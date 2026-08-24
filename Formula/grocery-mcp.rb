class GroceryMcp < Formula
  desc "Local MCP server for grocery shopping (REWE)"
  homepage "https://github.com/dennisschroeder/grocery-mcp"
  url "https://github.com/dennisschroeder/grocery-mcp/archive/refs/tags/v0.3.0.tar.gz"
  sha256 "eb7adb26743ffd11eeb2c5696867d25748e13e4190916c6df7946eb8e457e65f"
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/grocery-mcp"
    (share/"grocery-mcp").install "extension"
  end

  def caveats
    <<~EOS
      Load the unpacked Chrome extension from:
        #{opt_share}/grocery-mcp/extension

      Then register the native host for that extension's ID:
        grocery-mcp install-native-host --extension-id <ID>
    EOS
  end

  test do
    assert_match "unknown command", shell_output("#{bin}/grocery-mcp --help 2>&1", 1)
  end
end
