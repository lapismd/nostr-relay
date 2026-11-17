package agentbunker

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
	"zooid/internal/keywrap"
)

func TestManagedAgentSignsOnlyScopedJJAndGitTemplates(t *testing.T) {
	service, _, _ := testService(t)
	agentKey, owner, repositoryOwner := nostr.Generate(), nostr.Generate(), nostr.Generate()
	agent := Agent{Pubkey: agentKey.Public().Hex(), OwnerPubkey: owner.Public().Hex(), Status: StatusActive}
	now := nostr.Timestamp(service.Now().Unix())
	repository := nostr.Event{
		Kind: nostr.KindRepositoryAnnouncement, PubKey: repositoryOwner.Public(), CreatedAt: now,
		Tags: nostr.Tags{{"d", "notes"}, {"buzz-channel", "room"}, {"h", "room"}},
	}
	coordinate := "30617:" + repositoryOwner.Public().Hex() + ":notes"
	service.CurrentEvent = func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event {
		if kind == nostr.KindRepositoryAnnouncement && author == repositoryOwner.Public() && d == "notes" {
			return &repository
		}
		return nil
	}
	service.IsMember = func(group string, key nostr.PubKey) bool {
		return group == "room" && key == agentKey.Public()
	}

	jj := nostr.Event{Kind: 3908, CreatedAt: now, Tags: nostr.Tags{
		{"t", "jj-agent-context-page"}, {"r", "scope"}, {"o", "operation"},
		{"d", "checkpoint"}, {"sequence", "0"}, {"a", coordinate}, {"h", "room"},
	}, Content: `{"schema":"jj-sync/agent-context-page"}`}
	require.NoError(t, service.authorizeSigning(agent, jj))

	oldPage := jj
	oldPage.CreatedAt = now - 86400
	oldPage.Content = fmt.Sprintf(`{"schema":"jj-sync/agent-context-page","version":2,"records":[{"created_at_ms":%d}]}`, int64(oldPage.CreatedAt)*1000)
	require.NoError(t, service.authorizeSigning(agent, oldPage))
	mismatch := oldPage
	mismatch.CreatedAt++
	require.ErrorContains(t, service.authorizeSigning(agent, mismatch), "timestamp")
	liveOld := oldPage
	liveOld.Kind = 23908
	require.ErrorContains(t, service.authorizeSigning(agent, liveOld), "timestamp")
	future := oldPage
	future.CreatedAt = now + 86400
	future.Content = fmt.Sprintf(`{"schema":"jj-sync/agent-context-page","version":2,"records":[{"created_at_ms":%d}]}`, int64(future.CreatedAt)*1000)
	require.ErrorContains(t, service.authorizeSigning(agent, future), "timestamp")

	wrongRepository := jj
	wrongRepository.Tags = jj.Tags.CloneDeep()
	wrongRepository.Tags[5][1] = "30617:" + repositoryOwner.Public().Hex() + ":other"
	require.ErrorContains(t, service.authorizeSigning(agent, wrongRepository), "JJ context")

	extraTag := jj
	extraTag.Tags = append(jj.Tags.CloneDeep(), nostr.Tag{"client", "untrusted"})
	require.ErrorContains(t, service.authorizeSigning(agent, extraTag), "JJ context tags")

	gitAuth := nostr.Event{Kind: nostr.KindHTTPAuth, CreatedAt: now, Tags: nostr.Tags{
		{"u", "https://relay.example/git/" + repositoryOwner.Public().Hex() + "/notes"},
		{"method", http.MethodGet},
	}}
	require.NoError(t, service.authorizeSigning(agent, gitAuth))
	gitAuth.Tags[0][1] = "https://attacker.example/git/" + repositoryOwner.Public().Hex() + "/notes"
	require.ErrorContains(t, service.authorizeSigning(agent, gitAuth), "repository URL")
}

func TestManagedAgentSupportsOwnerOnlyNIP17(t *testing.T) {
	service, store, _ := testService(t)
	owner := nostr.Generate()
	agentSecret := nostr.Generate()
	clientSecret := nostr.Generate()
	now := service.Now()
	key, err := keywrap.Encrypt(agentSecret[:], service.agentAAD(agentSecret.Public().Hex(), owner.Public().Hex()), service.Wrapper)
	require.NoError(t, err)
	store.agents[agentSecret.Public().Hex()] = Agent{Pubkey: agentSecret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), RelayPubkey: service.Config.RelayPubkey.Hex(), Status: StatusActive, Key: key, CreatedAt: now}
	store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()] = Client{AgentPubkey: agentSecret.Public().Hex(), ClientPubkey: clientSecret.Public().Hex(), Status: ClientActive, CreatedAt: now}

	sign := func(id string, template eventTemplate) nip46.Response {
		raw, marshalErr := json.Marshal(template)
		require.NoError(t, marshalErr)
		request := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: id, Method: "sign_event", Params: []string{string(raw)}}, now)
		responseEvent, handleErr := service.HandleNIP46(context.Background(), request)
		require.NoError(t, handleErr)
		return decodeBunkerResponse(t, responseEvent, agentSecret.Public(), clientSecret)
	}

	inbox := sign("inbox", eventTemplate{Kind: int(nostr.KindDMRelayList), CreatedAt: now.Unix(), Tags: [][]string{{"relay", "wss://relay.example"}}, Content: ""})
	require.Empty(t, inbox.Error)
	require.NotEmpty(t, inbox.Result)
	badInbox := sign("bad-inbox", eventTemplate{Kind: int(nostr.KindDMRelayList), CreatedAt: now.Unix(), Tags: [][]string{{"relay", "wss://other.example"}}, Content: ""})
	require.Contains(t, badInbox.Error, "inbox relay list")

	seal := sign("seal", eventTemplate{Kind: int(nostr.KindSeal), CreatedAt: now.Add(-36 * time.Hour).Unix(), Tags: [][]string{}, Content: "ciphertext"})
	require.Empty(t, seal.Error)
	badSeal := sign("bad-seal", eventTemplate{Kind: int(nostr.KindSeal), CreatedAt: now.Unix(), Tags: [][]string{{"p", owner.Public().Hex()}}, Content: "ciphertext"})
	require.Contains(t, badSeal.Error, "invalid NIP-17 seal")

	rumor := `{"id":"rumor","pubkey":"` + owner.Public().Hex() + `","created_at":1,"kind":14,"tags":[["p","` + agentSecret.Public().Hex() + `"]],"content":"hello"}`
	ownerConversation, err := nip44.GenerateConversationKey(agentSecret.Public(), owner)
	require.NoError(t, err)
	sealedContent, err := nip44.Encrypt(rumor, ownerConversation)
	require.NoError(t, err)
	sealedEvent := nostr.Event{Kind: nostr.KindSeal, CreatedAt: nostr.Timestamp(now.Add(-time.Hour).Unix()), Tags: nostr.Tags{}, Content: sealedContent}
	require.NoError(t, sealedEvent.Sign(owner))
	sealedJSON, err := json.Marshal(sealedEvent)
	require.NoError(t, err)

	ephemeral := nostr.Generate()
	outerConversation, err := nip44.GenerateConversationKey(agentSecret.Public(), ephemeral)
	require.NoError(t, err)
	wrappedContent, err := nip44.Encrypt(string(sealedJSON), outerConversation)
	require.NoError(t, err)
	giftWrap := nostr.Event{Kind: nostr.KindGiftWrap, CreatedAt: nostr.Timestamp(now.Add(-time.Hour).Unix()), Tags: nostr.Tags{{"p", agentSecret.Public().Hex()}}, Content: wrappedContent}
	require.NoError(t, giftWrap.Sign(ephemeral))
	giftWrapJSON, err := json.Marshal(giftWrap)
	require.NoError(t, err)
	unwrapRequest := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "unwrap", Method: "lapis_nip17_unwrap", Params: []string{string(giftWrapJSON)}}, now)
	unwrapEvent, err := service.HandleNIP46(context.Background(), unwrapRequest)
	require.NoError(t, err)
	unwrap := decodeBunkerResponse(t, unwrapEvent, agentSecret.Public(), clientSecret)
	require.Empty(t, unwrap.Error)
	require.JSONEq(t, string(sealedJSON), unwrap.Result)

	selfRumor := nostr.Event{Kind: 14, PubKey: agentSecret.Public(), CreatedAt: 1, Tags: nostr.Tags{{"p", owner.Public().Hex()}}, Content: "hello"}
	selfRumor.ID = selfRumor.GetID()
	selfRumorJSON, err := json.Marshal(selfRumor)
	require.NoError(t, err)
	selfEncrypt := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "self-encrypt", Method: "nip44_encrypt", Params: []string{agentSecret.Public().Hex(), string(selfRumorJSON)}}, now)
	selfEncryptEvent, err := service.HandleNIP46(context.Background(), selfEncrypt)
	require.NoError(t, err)
	require.Empty(t, decodeBunkerResponse(t, selfEncryptEvent, agentSecret.Public(), clientSecret).Error)

	wrongRecipient := giftWrap
	wrongRecipient.Tags = nostr.Tags{{"p", owner.Public().Hex()}}
	require.NoError(t, wrongRecipient.Sign(ephemeral))
	wrongJSON, err := json.Marshal(wrongRecipient)
	require.NoError(t, err)
	wrongRequest := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "wrong-recipient", Method: "lapis_nip17_unwrap", Params: []string{string(wrongJSON)}}, now)
	wrongEvent, err := service.HandleNIP46(context.Background(), wrongRequest)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, wrongEvent, agentSecret.Public(), clientSecret).Error, "invalid NIP-17")
}

func TestManagedConversationSigning(t *testing.T) {
	service, _, _ := testService(t)
	secret, owner := nostr.Generate(), nostr.Generate()
	agent := Agent{Pubkey: secret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), Status: StatusActive}
	now := nostr.Timestamp(service.Now().Unix())
	root := nostr.Event{Kind: 11, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}}, Content: "Topic"}
	require.NoError(t, root.Sign(owner))
	reaction := nostr.Event{Kind: 7, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"e", root.ID.Hex()}}, Content: "👀"}
	require.NoError(t, reaction.Sign(secret))
	service.EventByID = func(id nostr.ID) *nostr.Event {
		if id == root.ID {
			return &root
		}
		if id == reaction.ID {
			return &reaction
		}
		return nil
	}
	service.IsMember = func(group string, key nostr.PubKey) bool { return group == "room" && key == secret.Public() }
	deletion := nostr.Event{Kind: 5, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"e", reaction.ID.Hex()}, {"k", "7"}}}
	require.NoError(t, service.authorizeSigning(agent, deletion))
	comment := nostr.Event{Kind: 1111, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"E", root.ID.Hex(), "", root.PubKey.Hex()}, {"K", "11"}, {"P", root.PubKey.Hex()}, {"e", root.ID.Hex(), "", root.PubKey.Hex()}, {"k", "11"}, {"p", root.PubKey.Hex()}}, Content: "Reply"}
	require.NoError(t, service.authorizeSigning(agent, comment))
	for _, kind := range []nostr.Kind{11, 1, 27235} {
		invalid := comment
		invalid.Kind = kind
		require.Error(t, service.authorizeSigning(agent, invalid))
	}
	invalid := deletion
	invalid.Tags = nostr.Tags{{"h", "room"}, {"e", root.ID.Hex()}, {"k", "7"}}
	require.Error(t, service.authorizeSigning(agent, invalid))
	reaction.PubKey = owner.Public()
	require.Error(t, service.authorizeSigning(agent, deletion))
	reaction.PubKey = secret.Public()
	root.Tags = nostr.Tags{{"h", "elsewhere"}}
	require.Error(t, service.authorizeSigning(agent, comment))
	root.Tags = nostr.Tags{{"h", "room"}}
	invalid = comment
	invalid.Tags = append(append(nostr.Tags{}, comment.Tags...), nostr.Tag{"E", root.ID.Hex()})
	require.Error(t, service.authorizeSigning(agent, invalid))
	service.IsMember = func(string, nostr.PubKey) bool { return false }
	require.Error(t, service.authorizeSigning(agent, comment))
	require.Error(t, service.authorizeSigning(agent, deletion))
}

func TestManagedRelayToolSigning(t *testing.T) {
	service, _, _ := testService(t)
	agentSecret, owner, repositoryOwner := nostr.Generate(), nostr.Generate(), nostr.Generate()
	agent := Agent{Pubkey: agentSecret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), Status: StatusActive}
	now := nostr.Timestamp(service.Now().Unix())
	repository := nostr.Event{
		Kind: nostr.KindRepositoryAnnouncement, PubKey: repositoryOwner.Public(), CreatedAt: now,
		Tags: nostr.Tags{{"d", "repo"}, {"name", "Repo"}, {"buzz-channel", "room"}, {"h", "room"}},
	}
	coordinate := "30617:" + repositoryOwner.Public().Hex() + ":repo"
	service.CurrentEvent = func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event {
		if kind == nostr.KindRepositoryAnnouncement && author == repositoryOwner.Public() && d == "repo" {
			return &repository
		}
		return nil
	}
	service.IsMember = func(group string, key nostr.PubKey) bool { return group == "room" && key == agentSecret.Public() }

	note := nostr.Event{Kind: 30023, CreatedAt: now, Tags: nostr.Tags{{"d", "note"}, {"title", "Note"}, {"h", "room"}}, Content: "Body"}
	require.NoError(t, service.authorizeSigning(agent, note))
	newRepository := nostr.Event{Kind: 30617, CreatedAt: now, Tags: nostr.Tags{{"d", "new-repo"}, {"name", "New repo"}, {"buzz-channel", "room"}, {"h", "room"}}}
	require.NoError(t, service.authorizeSigning(agent, newRepository))
	project := nostr.Event{Kind: 30621, CreatedAt: now, Tags: nostr.Tags{{"d", "project"}, {"name", "Project"}, {"buzz-channel", "room"}, {"h", "room"}, {"a", coordinate}}}
	require.NoError(t, service.authorizeSigning(agent, project))
	issue := nostr.Event{Kind: nostr.KindIssue, PubKey: repositoryOwner.Public(), CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"a", coordinate}, {"subject", "Issue"}}, Content: "Body"}
	require.NoError(t, issue.Sign(repositoryOwner))
	service.EventByID = func(id nostr.ID) *nostr.Event {
		if id == issue.ID {
			return &issue
		}
		return nil
	}
	issueTemplate := nostr.Event{Kind: nostr.KindIssue, CreatedAt: now, Tags: issue.Tags.CloneDeep(), Content: issue.Content}
	require.NoError(t, service.authorizeSigning(agent, issueTemplate))
	status := nostr.Event{Kind: nostr.KindStatusClosed, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"a", coordinate}, {"e", issue.ID.Hex(), "", "root"}}}
	require.NoError(t, service.authorizeSigning(agent, status))

	pullRequest := nostr.Event{Kind: 1618, PubKey: repositoryOwner.Public(), CreatedAt: now, Tags: nostr.Tags{{"a", coordinate}, {"h", "room"}}}
	require.NoError(t, pullRequest.Sign(repositoryOwner))
	service.EventByID = func(id nostr.ID) *nostr.Event {
		switch id {
		case issue.ID:
			return &issue
		case pullRequest.ID:
			return &pullRequest
		default:
			return nil
		}
	}
	comment := nostr.Event{Kind: 1111, CreatedAt: now, Tags: nostr.Tags{
		{"h", "room"}, {"a", coordinate},
		{"E", pullRequest.ID.Hex()}, {"K", "1618"}, {"P", repositoryOwner.Public().Hex()},
		{"e", pullRequest.ID.Hex()}, {"k", "1618"}, {"p", repositoryOwner.Public().Hex()},
	}, Content: "Review note"}
	require.NoError(t, service.authorizeSigning(agent, comment))

	invalid := project
	invalid.Tags[2][1] = "elsewhere"
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "invalid project template")
	invalid = status
	invalid.Tags[1][1] = "30617:" + repositoryOwner.Public().Hex() + ":missing"
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "invalid issue status template")
	invalid = comment
	invalid.Tags = append(invalid.Tags, nostr.Tag{"client", "untrusted"})
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "invalid pull-request comment template")
	invalid = comment
	invalid.Tags[3][1] = "1621"
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "comment root kind is not allowed")
	invalid = note
	invalid.Tags = append(invalid.Tags, nostr.Tag{"published_at", "1"})
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "invalid long-form note template")
}

func TestManagedNIP46UsesCurrentDirectMessageApprovals(t *testing.T) {
	service, store, _ := testService(t)
	owner, agentKey, peer, client := nostr.Generate(), nostr.Generate(), nostr.Generate(), nostr.Generate()
	now := service.Now()
	key, err := keywrap.Encrypt(agentKey[:], service.agentAAD(agentKey.Public().Hex(), owner.Public().Hex()), service.Wrapper)
	require.NoError(t, err)
	store.agents[agentKey.Public().Hex()] = Agent{Pubkey: agentKey.Public().Hex(), OwnerPubkey: owner.Public().Hex(), RelayPubkey: service.Config.RelayPubkey.Hex(), Status: StatusActive, Key: key, CreatedAt: now}
	store.clients[agentKey.Public().Hex()+":"+client.Public().Hex()] = Client{AgentPubkey: agentKey.Public().Hex(), ClientPubkey: client.Public().Hex(), Status: ClientActive, CreatedAt: now}
	policy, err := json.Marshal(map[string]any{"dm_allowlist": []string{peer.Public().Hex()}})
	require.NoError(t, err)
	instance := nostr.Event{Kind: 30177, CreatedAt: nostr.Timestamp(now.Unix()), Tags: nostr.Tags{{"d", agentKey.Public().Hex()}}, Content: string(policy)}
	require.NoError(t, instance.Sign(owner))
	service.CurrentEvent = func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event {
		if kind == 30177 && author == owner.Public() && d == agentKey.Public().Hex() {
			return &instance
		}
		return nil
	}
	rpc := func(id, method, peerKey, body string) nip46.Response {
		event, callErr := service.HandleNIP46(context.Background(), rpcEvent(t, agentKey.Public(), agentKey, client, nip46.Request{ID: id, Method: method, Params: []string{peerKey, body}}, now))
		require.NoError(t, callErr)
		return decodeBunkerResponse(t, event, agentKey.Public(), client)
	}
	rumor := nostr.Event{Kind: 14, PubKey: agentKey.Public(), CreatedAt: nostr.Timestamp(now.Unix()), Tags: nostr.Tags{{"p", peer.Public().Hex()}}, Content: "Approved private reply"}
	rumor.ID = rumor.GetID()
	encoded, err := json.Marshal(rumor)
	require.NoError(t, err)
	encrypted := rpc("approved-encrypt", "nip44_encrypt", peer.Public().Hex(), string(encoded))
	require.Empty(t, encrypted.Error)
	conversation, err := nip44.GenerateConversationKey(agentKey.Public(), peer)
	require.NoError(t, err)
	decoded, err := nip44.Decrypt(encrypted.Result, conversation)
	require.NoError(t, err)
	require.JSONEq(t, string(encoded), decoded)
	rumor.PubKey = peer.Public()
	rumor.Tags = nostr.Tags{{"p", agentKey.Public().Hex()}}
	rumor.ID = rumor.GetID()
	encoded, err = json.Marshal(rumor)
	require.NoError(t, err)
	ciphertext, err := nip44.Encrypt(string(encoded), conversation)
	require.NoError(t, err)
	received := rpc("approved-decrypt", "nip44_decrypt", peer.Public().Hex(), ciphertext)
	require.Empty(t, received.Error)
	require.JSONEq(t, string(encoded), received.Result)
	instance.Content = "{}"
	require.NoError(t, instance.Sign(owner))
	revoked := rpc("revoked-decrypt", "nip44_decrypt", peer.Public().Hex(), ciphertext)
	require.Contains(t, revoked.Error, "not approved by the owner")
}
