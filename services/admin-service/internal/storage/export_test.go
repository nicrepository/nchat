package storage

// ReauthorizeQueryForTest exposes the per-request authorization SQL so a test
// can assert which clauses it does and does not carry. The difference between
// this query and the handshake one is a deliberate security decision, and a
// test is the only thing that keeps it deliberate.
const ReauthorizeQueryForTest = reauthorizeQuery

// HandshakeQueryForTest exposes the sign-in SQL for the same reason: the two
// queries are deliberately different, and only a test keeps them that way.
var HandshakeQueryForTest = authorizeHandshakeQuery

// ConversationQueryForTest exposes the private-conversation SQL so a test can
// assert what it never selects. The promise that the Admin API exposes DM
// metadata and not DM content is a property of this query's projection, so the
// query text is what the regression guard has to read.
func ConversationQueryForTest() string { return listConversationsQuery }

// AddChannelMembersQueryForTest exposes the membership add statement so a test
// can assert that it embeds the shared eligibility rule rather than a second
// copy of it.
func AddChannelMembersQueryForTest() string { return addChannelMembersQuery }

// LikePatternForTest exposes the search-pattern builder so a test can assert
// the bytes it produces, which is the only representation the database sees.
func LikePatternForTest(query string) string { return likePattern(query) }

// ListUsersQueryForTest and ListChannelsQueryForTest expose the two searching
// queries so a test can assert that every ILIKE predicate states its escape
// character rather than relying on the server default.
func ListUsersQueryForTest() string    { return listUsersQuery }
func ListChannelsQueryForTest() string { return listChannelsQuery }

// MemberCandidateQueryForTest exposes the candidate search so a test can assert
// that it offers only what the shared eligibility rule would admit.
func MemberCandidateQueryForTest() string { return listMemberCandidatesQuery }

// LockChannelQueryForTest exposes the membership lock so a test can assert it
// still obeys the shared serialization protocol.
func LockChannelQueryForTest() string { return lockChannelQuery }
