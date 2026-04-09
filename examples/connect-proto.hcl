# Connect-RPC Proto Example
# Serves mock ConnectRPC endpoints generated from .proto definitions.
#
# Usage:
#   polymorph server -c examples/connect-proto.hcl
#
# Test with curl:
#   curl -X POST http://localhost:8080/bookstore.v1.BookService/GetBook \
#     -H 'Content-Type: application/json' -d '{"id": "abc"}'
#
#   curl -X POST http://localhost:8080/bookstore.v1.BookService/ListBooks \
#     -H 'Content-Type: application/json' -d '{}'
#
#   curl -X POST http://localhost:8080/bookstore.v1.ReviewService/ListReviews \
#     -H 'Content-Type: application/json' -d '{"bookId": "abc"}'
#
# Test with grpcurl:
#   grpcurl -plaintext -d '{"id":"abc"}' localhost:8080 bookstore.v1.BookService/GetBook
#   grpcurl -plaintext -d '{}' localhost:8080 bookstore.v1.BookService/ListBooks

service "connect" "bookstore" {
  listen = "0.0.0.0:8080"

  proto {
    path = "./examples/proto"
    rows = 5
    seed = 42
  }

  # Override GetBook with a custom response
  handle "GetBook" {
    response {
      body = jsonencode({
        id          = "custom-001"
        title       = "The Art of Mock Services"
        author      = { name = "Polymorph", bio = "A fake service generator" }
        genre       = "GENRE_SCIENCE"
        pages       = 256
        price       = 29.99
        inStock     = true
        tags        = ["testing", "mocking", "connectrpc"]
        publishedAt = timestamp()
      })
    }
  }
}
