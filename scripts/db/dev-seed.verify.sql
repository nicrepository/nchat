\set ON_ERROR_STOP on

DO $$
DECLARE
    mock_users integer;
    mock_channels integer;
    mock_messages integer;
    mock_conversations integer;
BEGIN
    SELECT count(*) INTO mock_users
    FROM auth.users
    WHERE email IN (
        'ana.silva@nchat.local',
        'bruno.costa@nchat.local',
        'carla.souza@nchat.local',
        'diego.lima@nchat.local',
        'fernanda.rocha@nchat.local'
    );

    SELECT count(*) INTO mock_channels
    FROM chat.channels
    WHERE workspace_id = '00000000-0000-0000-0000-000000000001'
      AND slug IN ('produto', 'engenharia', 'design', 'random', 'lideranca');

    SELECT count(*) INTO mock_messages
    FROM chat.messages
    WHERE id::text LIKE '30000000-0000-0000-0000-%';

    SELECT count(*) INTO mock_conversations
    FROM chat.dm_conversations
    WHERE id::text LIKE '40000000-0000-0000-0000-%';

    IF mock_users <> 5 THEN
        RAISE EXCEPTION 'expected 5 mock users, found %', mock_users;
    END IF;
    IF mock_channels <> 5 THEN
        RAISE EXCEPTION 'expected 5 mock channels, found %', mock_channels;
    END IF;
    IF mock_messages < 20 THEN
        RAISE EXCEPTION 'expected at least 20 mock messages, found %', mock_messages;
    END IF;
    IF mock_conversations <> 3 THEN
        RAISE EXCEPTION 'expected 3 mock conversations, found %', mock_conversations;
    END IF;
END $$;

SELECT 'development seed verified' AS result;
