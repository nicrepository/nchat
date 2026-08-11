\set ON_ERROR_STOP on

BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM auth.users WHERE email = 'admin@nchat.local') THEN
        RAISE EXCEPTION 'admin@nchat.local must exist before applying development seeds';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM auth.user_password_credentials pc
        JOIN auth.users u ON u.id = pc.user_id
        WHERE u.email = 'admin@nchat.local'
    ) THEN
        RAISE EXCEPTION 'admin@nchat.local must have password credentials';
    END IF;
END $$;

INSERT INTO auth.users (
    id, email, display_name, full_name, status, auth_source, email_verified_at
) VALUES
    ('10000000-0000-0000-0000-000000000001', 'ana.silva@nchat.local', 'Ana Silva', 'Ana Silva', 'active', 'manual', now()),
    ('10000000-0000-0000-0000-000000000002', 'bruno.costa@nchat.local', 'Bruno Costa', 'Bruno Costa', 'active', 'manual', now()),
    ('10000000-0000-0000-0000-000000000003', 'carla.souza@nchat.local', 'Carla Souza', 'Carla Souza', 'active', 'manual', now()),
    ('10000000-0000-0000-0000-000000000004', 'diego.lima@nchat.local', 'Diego Lima', 'Diego Lima', 'active', 'manual', now()),
    ('10000000-0000-0000-0000-000000000005', 'fernanda.rocha@nchat.local', 'Fernanda Rocha', 'Fernanda Rocha', 'active', 'manual', now())
ON CONFLICT (id) DO UPDATE SET
    email = EXCLUDED.email,
    display_name = EXCLUDED.display_name,
    full_name = EXCLUDED.full_name,
    status = 'active',
    deleted_at = NULL,
    updated_at = now();

INSERT INTO auth.user_password_credentials (user_id, password_hash, must_change_password)
SELECT mock_user.id, admin_credentials.password_hash, false
FROM auth.users mock_user
CROSS JOIN LATERAL (
    SELECT pc.password_hash
    FROM auth.user_password_credentials pc
    JOIN auth.users admin_user ON admin_user.id = pc.user_id
    WHERE admin_user.email = 'admin@nchat.local'
) admin_credentials
WHERE mock_user.email IN (
    'ana.silva@nchat.local',
    'bruno.costa@nchat.local',
    'carla.souza@nchat.local',
    'diego.lima@nchat.local',
    'fernanda.rocha@nchat.local'
)
ON CONFLICT (user_id) DO UPDATE SET
    password_hash = EXCLUDED.password_hash,
    must_change_password = false,
    updated_at = now();

INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
SELECT
    '00000000-0000-0000-0000-000000000001',
    u.id,
    CASE WHEN u.email = 'fernanda.rocha@nchat.local' THEN 'admin' ELSE 'member' END,
    'active'
FROM auth.users u
WHERE u.email IN (
    'ana.silva@nchat.local',
    'bruno.costa@nchat.local',
    'carla.souza@nchat.local',
    'diego.lima@nchat.local',
    'fernanda.rocha@nchat.local'
)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET
    role = EXCLUDED.role,
    status = 'active';

INSERT INTO chat.channel_categories (id, workspace_id, name, position) VALUES
    ('11000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001', 'Times', 10),
    ('11000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001', 'Projetos', 20),
    ('11000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000001', 'Social', 30)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    position = EXCLUDED.position,
    updated_at = now();

INSERT INTO chat.channels (
    id, workspace_id, category_id, slug, display_name, type, status, position, created_by
) VALUES
    ('20000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001', '11000000-0000-0000-0000-000000000002', 'produto', 'Produto', 'public', 'active', 10, '10000000-0000-0000-0000-000000000001'),
    ('20000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001', '11000000-0000-0000-0000-000000000001', 'engenharia', 'Engenharia', 'public', 'active', 20, '10000000-0000-0000-0000-000000000002'),
    ('20000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000001', '11000000-0000-0000-0000-000000000001', 'design', 'Design', 'public', 'active', 30, '10000000-0000-0000-0000-000000000003'),
    ('20000000-0000-0000-0000-000000000004', '00000000-0000-0000-0000-000000000001', '11000000-0000-0000-0000-000000000003', 'random', 'Random', 'public', 'active', 40, '10000000-0000-0000-0000-000000000004'),
    ('20000000-0000-0000-0000-000000000005', '00000000-0000-0000-0000-000000000001', NULL, 'lideranca', 'Liderança', 'private', 'active', 50, (SELECT id FROM auth.users WHERE email = 'admin@nchat.local'))
ON CONFLICT (id) DO UPDATE SET
    category_id = EXCLUDED.category_id,
    display_name = EXCLUDED.display_name,
    type = EXCLUDED.type,
    status = 'active',
    position = EXCLUDED.position,
    updated_at = now();

INSERT INTO chat.channel_members (channel_id, user_id, role)
SELECT '20000000-0000-0000-0000-000000000005', u.id,
       CASE WHEN u.email = 'admin@nchat.local' THEN 'moderator' ELSE 'member' END
FROM auth.users u
WHERE u.email IN ('admin@nchat.local', 'fernanda.rocha@nchat.local')
ON CONFLICT (channel_id, user_id) DO UPDATE SET role = EXCLUDED.role;

INSERT INTO chat.dm_conversations (
    id, workspace_id, type, title, status, created_by, direct_pair_key, updated_at
)
SELECT
    values_to_insert.id,
    '00000000-0000-0000-0000-000000000001',
    values_to_insert.type,
    values_to_insert.title,
    'active',
    values_to_insert.created_by,
    values_to_insert.direct_pair_key,
    now()
FROM (
    SELECT
        '40000000-0000-0000-0000-000000000001'::uuid AS id,
        'direct'::text AS type,
        NULL::text AS title,
        admin_user.id AS created_by,
        '36:' || LEAST(admin_user.id::text, ana.id::text) || '36:' || GREATEST(admin_user.id::text, ana.id::text) AS direct_pair_key
    FROM auth.users admin_user, auth.users ana
    WHERE admin_user.email = 'admin@nchat.local' AND ana.email = 'ana.silva@nchat.local'
    UNION ALL
    SELECT
        '40000000-0000-0000-0000-000000000002'::uuid,
        'direct',
        NULL,
        admin_user.id,
        '36:' || LEAST(admin_user.id::text, bruno.id::text) || '36:' || GREATEST(admin_user.id::text, bruno.id::text)
    FROM auth.users admin_user, auth.users bruno
    WHERE admin_user.email = 'admin@nchat.local' AND bruno.email = 'bruno.costa@nchat.local'
    UNION ALL
    SELECT
        '40000000-0000-0000-0000-000000000003'::uuid,
        'group',
        'Squad Lançamento',
        admin_user.id,
        NULL
    FROM auth.users admin_user
    WHERE admin_user.email = 'admin@nchat.local'
) values_to_insert
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    status = 'active',
    direct_pair_key = EXCLUDED.direct_pair_key,
    updated_at = now();

INSERT INTO chat.dm_members (conversation_id, user_id, role, status)
SELECT membership.conversation_id, u.id, 'member', 'active'
FROM (
    VALUES
        ('40000000-0000-0000-0000-000000000001'::uuid, 'admin@nchat.local'),
        ('40000000-0000-0000-0000-000000000001'::uuid, 'ana.silva@nchat.local'),
        ('40000000-0000-0000-0000-000000000002'::uuid, 'admin@nchat.local'),
        ('40000000-0000-0000-0000-000000000002'::uuid, 'bruno.costa@nchat.local'),
        ('40000000-0000-0000-0000-000000000003'::uuid, 'admin@nchat.local'),
        ('40000000-0000-0000-0000-000000000003'::uuid, 'ana.silva@nchat.local'),
        ('40000000-0000-0000-0000-000000000003'::uuid, 'carla.souza@nchat.local'),
        ('40000000-0000-0000-0000-000000000003'::uuid, 'fernanda.rocha@nchat.local')
) membership(conversation_id, email)
JOIN auth.users u ON u.email = membership.email
ON CONFLICT (conversation_id, user_id) DO UPDATE SET
    status = 'active',
    left_at = NULL;

INSERT INTO chat.messages (
    id, workspace_id, channel_id, dm_conversation_id, sender_id, body_text, created_at, updated_at
) VALUES
    ('30000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', NULL, '10000000-0000-0000-0000-000000000001', 'Bom dia, pessoal! Bem-vindos ao NChat 👋', now() - interval '3 days', now() - interval '3 days'),
    ('30000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', NULL, '10000000-0000-0000-0000-000000000002', 'Hoje temos nossa primeira demonstração interna.', now() - interval '2 days 20 hours', now() - interval '2 days 20 hours'),
    ('30000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', NULL, '10000000-0000-0000-0000-000000000003', 'A interface ficou muito boa no tema escuro.', now() - interval '2 days', now() - interval '2 days'),
    ('30000000-0000-0000-0000-000000000004', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001', NULL, '10000000-0000-0000-0000-000000000001', 'Prioridades da semana: onboarding, busca e notificações.', now() - interval '30 hours', now() - interval '30 hours'),
    ('30000000-0000-0000-0000-000000000005', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001', NULL, '10000000-0000-0000-0000-000000000003', 'Preparei os novos estados vazios para revisão.', now() - interval '26 hours', now() - interval '26 hours'),
    ('30000000-0000-0000-0000-000000000006', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001', NULL, '10000000-0000-0000-0000-000000000005', 'Vou consolidar o feedback do piloto amanhã.', now() - interval '22 hours', now() - interval '22 hours'),
    ('30000000-0000-0000-0000-000000000007', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000002', NULL, '10000000-0000-0000-0000-000000000002', 'API de mensagens está estável no ambiente local.', now() - interval '20 hours', now() - interval '20 hours'),
    ('30000000-0000-0000-0000-000000000008', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000002', NULL, '10000000-0000-0000-0000-000000000004', 'Estou revisando os eventos de WebSocket.', now() - interval '18 hours', now() - interval '18 hours'),
    ('30000000-0000-0000-0000-000000000009', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000002', NULL, '10000000-0000-0000-0000-000000000001', 'Ótimo, depois podemos validar a reconexão.', now() - interval '17 hours', now() - interval '17 hours'),
    ('30000000-0000-0000-0000-000000000010', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000003', NULL, '10000000-0000-0000-0000-000000000003', 'Atualizei os componentes de anexos e vídeo.', now() - interval '15 hours', now() - interval '15 hours'),
    ('30000000-0000-0000-0000-000000000011', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000003', NULL, '10000000-0000-0000-0000-000000000001', 'Vamos manter o contraste e a navegação por teclado.', now() - interval '14 hours', now() - interval '14 hours'),
    ('30000000-0000-0000-0000-000000000012', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000004', NULL, '10000000-0000-0000-0000-000000000004', 'Alguém aceita um café antes da daily? ☕', now() - interval '12 hours', now() - interval '12 hours'),
    ('30000000-0000-0000-0000-000000000013', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000004', NULL, '10000000-0000-0000-0000-000000000002', 'Sempre! Cinco minutos na cozinha.', now() - interval '11 hours 50 minutes', now() - interval '11 hours 50 minutes'),
    ('30000000-0000-0000-0000-000000000014', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000005', NULL, '10000000-0000-0000-0000-000000000005', 'Resumo executivo disponível para a liderança.', now() - interval '10 hours', now() - interval '10 hours'),
    ('30000000-0000-0000-0000-000000000015', '00000000-0000-0000-0000-000000000001', NULL, '40000000-0000-0000-0000-000000000001', '10000000-0000-0000-0000-000000000001', 'Oi! Conseguiu testar o novo fluxo?', now() - interval '9 hours', now() - interval '9 hours'),
    ('30000000-0000-0000-0000-000000000016', '00000000-0000-0000-0000-000000000001', NULL, '40000000-0000-0000-0000-000000000001', (SELECT id FROM auth.users WHERE email = 'admin@nchat.local'), 'Sim, funcionou bem aqui no Windows.', now() - interval '8 hours 50 minutes', now() - interval '8 hours 50 minutes'),
    ('30000000-0000-0000-0000-000000000017', '00000000-0000-0000-0000-000000000001', NULL, '40000000-0000-0000-0000-000000000002', '10000000-0000-0000-0000-000000000002', 'Pode revisar o PR de busca quando tiver tempo?', now() - interval '7 hours', now() - interval '7 hours'),
    ('30000000-0000-0000-0000-000000000018', '00000000-0000-0000-0000-000000000001', NULL, '40000000-0000-0000-0000-000000000002', (SELECT id FROM auth.users WHERE email = 'admin@nchat.local'), 'Claro, vou olhar depois do almoço.', now() - interval '6 hours 45 minutes', now() - interval '6 hours 45 minutes'),
    ('30000000-0000-0000-0000-000000000019', '00000000-0000-0000-0000-000000000001', NULL, '40000000-0000-0000-0000-000000000003', '10000000-0000-0000-0000-000000000003', 'Checklist do lançamento atualizado.', now() - interval '5 hours', now() - interval '5 hours'),
    ('30000000-0000-0000-0000-000000000020', '00000000-0000-0000-0000-000000000001', NULL, '40000000-0000-0000-0000-000000000003', '10000000-0000-0000-0000-000000000005', 'Treinamento dos usuários marcado para sexta.', now() - interval '4 hours', now() - interval '4 hours'),
    ('30000000-0000-0000-0000-000000000021', '00000000-0000-0000-0000-000000000001', NULL, '40000000-0000-0000-0000-000000000003', (SELECT id FROM auth.users WHERE email = 'admin@nchat.local'), 'Perfeito. Vou preparar a demonstração.', now() - interval '3 hours', now() - interval '3 hours'),
    ('30000000-0000-0000-0000-000000000022', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001', NULL, '10000000-0000-0000-0000-000000000001', 'A reunião de discovery começa às 14h.', now() - interval '2 hours', now() - interval '2 hours'),
    ('30000000-0000-0000-0000-000000000023', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000002', NULL, '10000000-0000-0000-0000-000000000004', 'Pipeline local validado sem WSL.', now() - interval '1 hour', now() - interval '1 hour'),
    ('30000000-0000-0000-0000-000000000024', '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', NULL, (SELECT id FROM auth.users WHERE email = 'admin@nchat.local'), 'Ambiente de demonstração pronto para uso.', now() - interval '15 minutes', now() - interval '15 minutes')
ON CONFLICT (id) DO UPDATE SET
    body_text = EXCLUDED.body_text,
    status = 'active',
    deleted_at = NULL,
    updated_at = EXCLUDED.updated_at;

INSERT INTO chat.message_reactions (message_id, user_id, emoji) VALUES
    ('30000000-0000-0000-0000-000000000004', '10000000-0000-0000-0000-000000000003', '👍'),
    ('30000000-0000-0000-0000-000000000004', '10000000-0000-0000-0000-000000000005', '🚀'),
    ('30000000-0000-0000-0000-000000000012', '10000000-0000-0000-0000-000000000002', '☕'),
    ('30000000-0000-0000-0000-000000000021', '10000000-0000-0000-0000-000000000001', '🎉')
ON CONFLICT DO NOTHING;

INSERT INTO chat.message_pins (target_type, target_id, message_id, pinned_by_user_id)
SELECT 'channel', '20000000-0000-0000-0000-000000000001', '30000000-0000-0000-0000-000000000004', u.id
FROM auth.users u
WHERE u.email = 'admin@nchat.local'
ON CONFLICT DO NOTHING;

INSERT INTO chat.message_favorites (user_id, message_id)
SELECT u.id, '30000000-0000-0000-0000-000000000010'
FROM auth.users u
WHERE u.email = 'admin@nchat.local'
ON CONFLICT DO NOTHING;

COMMIT;

\echo 'Development seed applied successfully.'
