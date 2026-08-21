package storage

// ReauthorizeQueryForTest exposes the per-request authorization SQL so a test
// can assert which clauses it does and does not carry. The difference between
// this query and the handshake one is a deliberate security decision, and a
// test is the only thing that keeps it deliberate.
const ReauthorizeQueryForTest = reauthorizeQuery

// HandshakeQueryForTest exposes the sign-in SQL for the same reason: the two
// queries are deliberately different, and only a test keeps them that way.
var HandshakeQueryForTest = authorizeHandshakeQuery
