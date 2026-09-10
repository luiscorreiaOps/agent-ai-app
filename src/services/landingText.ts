// Static UI chrome for the chat landing screen (greeting, subtitle, default
// quick prompts) in every language the "Default reply language" setting
// supports. This is separate from the LLM's own reply language (which the
// backend system prompt already handles per-request) -- these strings never
// go through the model, so they need their own translation, not a prompt.
export type ResponseLanguage = 'english' | 'portuguese' | 'spanish' | 'chinese' | 'french';

interface QuickPromptText {
    title: string;
    content: string;
}

interface LandingText {
    greetingMorning: string;
    greetingAfternoon: string;
    greetingEvening: string;
    subtitle: string;
    quickPrompts: {
        introduction: QuickPromptText;
        incidents: QuickPromptText;
        information: QuickPromptText;
    };
}

const LANDING_TEXT: Record<ResponseLanguage, LandingText> = {
    chinese: {
        greetingMorning: '早上好',
        greetingAfternoon: '下午好',
        greetingEvening: '晚上好',
        subtitle: '欢迎使用 Agent AI！今天我能帮你什么？',
        quickPrompts: {
            introduction: {
                title: '新手入门',
                content: '我是新来的。请列出这个 Grafana 实例中最上层的文件夹和仪表盘,并建议一个我可以问的好问题。',
            },
            incidents: {
                title: '事件',
                content: '现在有正在触发的告警吗?如果有,请总结一下。',
            },
            information: {
                title: '信息',
                content: '列出这个 Grafana 实例中可用的数据源。',
            },
        },
    },
    english: {
        greetingMorning: 'Good morning',
        greetingAfternoon: 'Good afternoon',
        greetingEvening: 'Good evening',
        subtitle: 'Welcome to Agent AI! How can I help you today?',
        quickPrompts: {
            introduction: {
                title: 'Getting started',
                content: 'I\'m new here. List the top-level folders and dashboards in this Grafana instance, and suggest one good first question I could ask.',
            },
            incidents: {
                title: 'Incidents',
                content: 'Are there any firing alerts right now? If so, summarize them.',
            },
            information: {
                title: 'Information',
                content: 'List the datasources available in this Grafana instance.',
            },
        },
    },
    portuguese: {
        greetingMorning: 'Bom dia',
        greetingAfternoon: 'Boa tarde',
        greetingEvening: 'Boa noite',
        subtitle: 'Bem-vindo ao Agent AI! Como posso te ajudar hoje?',
        quickPrompts: {
            introduction: {
                title: 'Primeiros passos',
                content: 'Sou novo aqui. Liste as pastas e dashboards de nível superior desta instância do Grafana, e sugira uma boa primeira pergunta que eu poderia fazer.',
            },
            incidents: {
                title: 'Incidentes',
                content: 'Existe algum alerta disparando agora? Se sim, resuma-os.',
            },
            information: {
                title: 'Informações',
                content: 'Liste as fontes de dados disponíveis nesta instância do Grafana.',
            },
        },
    },
    french: {
        greetingMorning: 'Bonjour',
        greetingAfternoon: 'Bon après-midi',
        greetingEvening: 'Bonsoir',
        subtitle: 'Bienvenue sur Agent AI ! Comment puis-je vous aider aujourd’hui ?',
        quickPrompts: {
            introduction: {
                title: 'Premiers pas',
                content: 'Je débute ici. Liste les dossiers et tableaux de bord de premier niveau de cette instance Grafana, puis suggère une bonne première question à poser.',
            },
            incidents: {
                title: 'Incidents',
                content: 'Y a-t-il des alertes actives en ce moment ? Si oui, résume-les.',
            },
            information: {
                title: 'Informations',
                content: 'Liste les sources de données disponibles sur cette instance Grafana.',
            },
        },
    },
    spanish: {
        greetingMorning: 'Buenos días',
        greetingAfternoon: 'Buenas tardes',
        greetingEvening: 'Buenas noches',
        subtitle: '¡Bienvenido a Agent AI! ¿Cómo puedo ayudarte hoy?',
        quickPrompts: {
            introduction: {
                title: 'Primeros pasos',
                content: 'Soy nuevo aquí. Enumera las carpetas y paneles de nivel superior de esta instancia de Grafana, y sugiere una buena primera pregunta que podría hacer.',
            },
            incidents: {
                title: 'Incidentes',
                content: '¿Hay alguna alerta activa en este momento? Si es así, resúmelas.',
            },
            information: {
                title: 'Información',
                content: 'Enumera las fuentes de datos disponibles en esta instancia de Grafana.',
            },
        },
    },
};

export function normalizeResponseLanguage(value: unknown): ResponseLanguage {
    return value === 'portuguese' || value === 'spanish' || value === 'chinese' || value === 'french'
        ? value
        : 'english';
}

export function getLandingText(language: ResponseLanguage): LandingText {
    return LANDING_TEXT[language];
}
